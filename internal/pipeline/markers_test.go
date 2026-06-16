package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/keithah/tidemark/internal/marker"
)

func TestSendMarkerReturnsContextErrorWhenReceiverUnavailable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan *marker.Marker)
	err := SendMarker(ctx, ch, &marker.Marker{Type: marker.MarkerICY})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendMarker error = %v, want context.Canceled", err)
	}
}

func TestSendMarkerDoesNotEnqueueWhenContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := make(chan *marker.Marker, 1)
	err := SendMarker(ctx, ch, &marker.Marker{Type: marker.MarkerICY})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SendMarker error = %v, want context.Canceled", err)
	}
	if len(ch) != 0 {
		t.Fatal("SendMarker enqueued marker after context cancellation")
	}
}
