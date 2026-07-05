package mpegts

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/futzu/cuei"

	"github.com/keithah/tidemark/internal/id3"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/pipeline"
	"github.com/keithah/tidemark/internal/scte35"
)

// tsPacketSize is the size of a single MPEGTS transport packet.
const tsPacketSize = 188

// tsSyncByte marks the start of every MPEGTS transport packet.
const tsSyncByte = 0x47

// Decoder wraps cuei.Stream for MPEGTS SCTE-35 decoding.
type Decoder struct {
	stream *cuei.Stream
}

// SegmentResult contains all metadata extracted from one MPEG-TS segment.
type SegmentResult struct {
	SCTE35 []*marker.Marker
	ID3    [][]id3.Tag
}

// NewDecoder creates a new MPEGTS decoder.
func NewDecoder() *Decoder {
	s := cuei.NewStream()
	s.Quiet = true
	return &Decoder{stream: s}
}

// DecodeBuf decodes SCTE-35 from a raw MPEGTS buffer.
// Returns any markers found. Recovers from cuei panics and reports them.
func (d *Decoder) DecodeBuf(data []byte) (markers []*marker.Marker, err error) {
	if len(data) == 0 {
		return nil, nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			markers = nil
			err = fmt.Errorf("decode MPEGTS: %v", recovered)
		}
	}()

	cues := d.stream.DecodeBytes(data)
	markers = make([]*marker.Marker, 0, len(cues))
	for _, cue := range cues {
		m := scte35.MarkerFromCue(cue, "mpegts", "")
		if m != nil {
			markers = append(markers, m)
		}
	}

	return markers, nil
}

// DecodeSegment extracts SCTE-35 and timed-ID3 markers from one segment. For
// non-TS inputs it skips SCTE-35 decoding and falls back to raw ID3 parsing.
func (d *Decoder) DecodeSegment(data []byte) (SegmentResult, error) {
	packets, _ := alignPackets(data)
	if len(packets) == 0 {
		groups, err := id3.ParseFromMPEGTS(data)
		return SegmentResult{ID3: groups}, err
	}

	markers, err := d.DecodeBuf(packets)
	if err != nil {
		return SegmentResult{}, err
	}
	return SegmentResult{
		SCTE35: markers,
		ID3:    mustParseID3FromMPEGTS(packets),
	}, nil
}

// DecodeSegmentReader streams one finite MPEG-TS segment and extracts SCTE-35
// plus timed ID3 without buffering the whole segment first.
func (d *Decoder) DecodeSegmentReader(ctx context.Context, r io.Reader) (SegmentResult, error) {
	buf := make([]byte, tsPacketSize*350)
	var leftover []byte
	var extractor id3.MPEGTSExtractor
	var result SegmentResult

	for {
		select {
		case <-ctx.Done():
			return SegmentResult{}, ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if n > 0 {
			data := buf[:n]
			if len(leftover) > 0 {
				data = append(leftover, data...)
			}
			packets, rest := alignPackets(data)
			leftover = append(leftover[:0], rest...)
			if len(packets) > 0 {
				for i := 0; i+tsPacketSize <= len(packets); i += tsPacketSize {
					extractor.PushPacket(packets[i : i+tsPacketSize])
				}
				markers, derr := d.DecodeBuf(packets)
				if derr != nil {
					return SegmentResult{}, derr
				}
				result.SCTE35 = append(result.SCTE35, markers...)
			}
		}
		if err == io.EOF {
			result.ID3 = extractor.Groups()
			return result, nil
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return SegmentResult{}, ctxErr
			}
			return SegmentResult{}, fmt.Errorf("read: %w", err)
		}
	}
}

// DecodeReader reads from an io.Reader in chunks and decodes SCTE-35.
// Emits markers on the channel. Does NOT close the channel.
func (d *Decoder) DecodeReader(ctx context.Context, r io.Reader, ch chan<- *marker.Marker) error {
	stopCloseOnCancel := closeOnCancel(ctx, r)
	defer stopCloseOnCancel()

	buf := make([]byte, tsPacketSize*350) // ~64KB, a whole number of TS packets

	// leftover holds bytes that did not form a complete, sync-aligned packet on
	// the previous read. Readers (notably net/http) do not guarantee that each
	// Read returns a multiple of the 188-byte packet size, so we must carry the
	// partial tail forward instead of discarding it. Dropping it would shift the
	// next chunk off the packet boundary, and the misalignment cascades until
	// cuei reads past a slice bound and panics.
	var leftover []byte

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if n > 0 {
			data := buf[:n]
			if len(leftover) > 0 {
				data = append(leftover, data...)
			}

			packets, rest := alignPackets(data)
			// Copy the remainder into its own backing array; `rest` aliases
			// either buf or the appended slice, both of which get reused.
			leftover = append(leftover[:0], rest...)

			if len(packets) > 0 {
				markers, derr := d.DecodeBuf(packets)
				if derr != nil {
					return derr
				}
				for _, m := range markers {
					if err := pipeline.SendMarker(ctx, ch, m); err != nil {
						return err
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return fmt.Errorf("read: %w", err)
		}
	}
}

// alignPackets returns the leading run of complete, sync-aligned MPEGTS packets
// in data along with any trailing bytes that do not yet form a full packet. It
// skips any bytes before the first sync byte so a stream that starts (or drifts)
// off a packet boundary can resynchronize.
func alignPackets(data []byte) (packets, rest []byte) {
	for search := 0; search < len(data); {
		rel := bytes.IndexByte(data[search:], tsSyncByte)
		if rel < 0 {
			// No sync byte left; nothing to decode and nothing worth carrying.
			return nil, nil
		}
		start := search + rel
		candidate := data[start:]

		// When a following packet boundary is available, require it to also hold
		// a sync byte before committing to this offset. Otherwise a stray 0x47 in
		// packet payload can lock the decoder onto a false phase, so that every
		// subsequent 188-byte "packet" is garbage and cuei misparses (or panics,
		// which DecodeReader surfaces as a fatal error) with no way to resync.
		if len(candidate) >= 2*tsPacketSize && candidate[tsPacketSize] != tsSyncByte {
			search = start + 1
			continue
		}

		full := (len(candidate) / tsPacketSize) * tsPacketSize
		return candidate[:full], candidate[full:]
	}
	return nil, nil
}

func mustParseID3FromMPEGTS(data []byte) [][]id3.Tag {
	groups, err := id3.ParseFromMPEGTS(data)
	if err != nil {
		return nil
	}
	return groups
}

func closeOnCancel(ctx context.Context, r io.Reader) func() {
	closer, ok := r.(io.Closer)
	if !ok {
		return func() {}
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = closer.Close()
		case <-done:
		}
	}()
	return func() {
		close(done)
	}
}
