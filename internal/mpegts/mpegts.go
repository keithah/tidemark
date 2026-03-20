package mpegts

import (
	"context"
	"fmt"
	"io"

	"github.com/futzu/cuei"

	"github.com/keithah/tidemark/internal/marker"
)

// Decoder wraps cuei.Stream for MPEGTS SCTE-35 decoding.
type Decoder struct {
	stream *cuei.Stream
}

// NewDecoder creates a new MPEGTS decoder.
func NewDecoder() *Decoder {
	s := cuei.NewStream()
	s.Quiet = true
	return &Decoder{stream: s}
}

// DecodeBuf decodes SCTE-35 from a raw MPEGTS buffer.
// Returns any markers found. Recovers from cuei panics.
func (d *Decoder) DecodeBuf(data []byte) []*marker.Marker {
	if len(data) == 0 {
		return nil
	}

	var markers []*marker.Marker
	var decodeErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				decodeErr = fmt.Errorf("cuei panic: %v", r)
			}
		}()

		cues := d.stream.DecodeBytes(data)
		for _, cue := range cues {
			m := cueToMarker(cue)
			if m != nil {
				markers = append(markers, m)
			}
		}
	}()

	if decodeErr != nil {
		// Log but don't fail — resilient pipeline
		_ = decodeErr
	}

	return markers
}

// DecodeReader reads from an io.Reader in chunks and decodes SCTE-35.
// Emits markers on the channel. Does NOT close the channel.
func (d *Decoder) DecodeReader(ctx context.Context, r io.Reader, ch chan<- *marker.Marker) error {
	buf := make([]byte, 32*1024) // 32KB chunks

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if n > 0 {
			markers := d.DecodeBuf(buf[:n])
			for _, m := range markers {
				ch <- m
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
}

func cueToMarker(cue *cuei.Cue) *marker.Marker {
	if cue == nil {
		return nil
	}

	fields := make(map[string]string)

	if cue.Command != nil {
		fields["CommandName"] = cue.Command.Name

		if cue.Command.Name == "Splice Insert" {
			fields["OutOfNetworkIndicator"] = fmt.Sprintf("%v", cue.Command.OutOfNetworkIndicator)
			if cue.Command.BreakDuration > 0 {
				fields["BreakDuration"] = fmt.Sprintf("%.3f", cue.Command.BreakDuration)
			}
			fields["SpliceEventID"] = fmt.Sprintf("0x%x", cue.Command.SpliceEventID)
		}
	}

	var pts float64
	if cue.Command != nil && cue.Command.PTS > 0 {
		pts = cue.Command.PTS
	}

	if len(cue.Descriptors) > 0 {
		for _, desc := range cue.Descriptors {
			if desc.SegmentationTypeID > 0 {
				fields["SegmentationTypeID"] = fmt.Sprintf("0x%02x", desc.SegmentationTypeID)
			}
		}
	}

	return &marker.Marker{
		Type:   marker.MarkerSCTE35,
		Source: "mpegts",
		PTS:    pts,
		Fields: fields,
	}
}
