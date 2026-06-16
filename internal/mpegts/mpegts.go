package mpegts

import (
	"context"
	"fmt"
	"io"

	"github.com/futzu/cuei"

	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/pipeline"
	"github.com/keithah/tidemark/internal/scte35"
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

// DecodeReader reads from an io.Reader in chunks and decodes SCTE-35.
// Emits markers on the channel. Does NOT close the channel.
func (d *Decoder) DecodeReader(ctx context.Context, r io.Reader, ch chan<- *marker.Marker) error {
	stopCloseOnCancel := closeOnCancel(ctx, r)
	defer stopCloseOnCancel()

	buf := make([]byte, 32*1024) // 32KB chunks

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		n, err := r.Read(buf)
		if n > 0 {
			markers, err := d.DecodeBuf(buf[:n])
			if err != nil {
				return err
			}
			for _, m := range markers {
				if err := pipeline.SendMarker(ctx, ch, m); err != nil {
					return err
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
