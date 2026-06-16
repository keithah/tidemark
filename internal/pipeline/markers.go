package pipeline

import (
	"context"

	"github.com/keithah/tidemark/internal/marker"
)

// SendMarker delivers a marker unless the context is canceled first.
func SendMarker(ctx context.Context, ch chan<- *marker.Marker, m *marker.Marker) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case ch <- m:
		return nil
	}
}
