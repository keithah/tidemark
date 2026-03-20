package mpegts

import (
	"bytes"
	"context"
	"io"
	"testing"
	"github.com/keithah/tidemark/internal/marker"
)

func TestNewDecoder(t *testing.T) {
	d := NewDecoder()
	if d == nil {
		t.Fatal("NewDecoder returned nil")
	}
	if d.stream == nil {
		t.Fatal("stream is nil")
	}
}

func TestDecodeBufEmpty(t *testing.T) {
	d := NewDecoder()
	markers := d.DecodeBuf(nil)
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for nil input, got %d", len(markers))
	}
}

func TestDecodeBufEmptySlice(t *testing.T) {
	d := NewDecoder()
	markers := d.DecodeBuf([]byte{})
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for empty input, got %d", len(markers))
	}
}

func TestDecodeBufPanicRecovery(t *testing.T) {
	d := NewDecoder()
	// Garbage data should not panic
	markers := d.DecodeBuf([]byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB})
	// We don't expect markers from garbage, just no panic
	_ = markers
}

func TestDecodeBufNoSCTE35(t *testing.T) {
	d := NewDecoder()
	// Valid-ish MPEGTS sync bytes but no SCTE-35
	data := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 188 bytes = 1 packet
	markers := d.DecodeBuf(data)
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for non-SCTE35 data, got %d", len(markers))
	}
}

func TestDecodeReaderContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		// Write some data then keep pipe open
		pw.Write(bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47))
	}()

	ctx, cancel := context.WithCancel(context.Background())
	d := NewDecoder()
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- d.DecodeReader(ctx, pr, ch)
	}()

	cancel()
	pw.Close()

	err := <-done
	if err != nil && err != context.Canceled {
		t.Logf("error: %v (acceptable)", err)
	}
}

func TestDecodeReaderEOF(t *testing.T) {
	data := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 1 MPEGTS packet
	d := NewDecoder()
	ch := make(chan *marker.Marker, 10)

	err := d.DecodeReader(context.Background(), bytes.NewReader(data), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCueToMarkerNil(t *testing.T) {
	m := cueToMarker(nil)
	if m != nil {
		t.Error("expected nil for nil cue")
	}
}

func TestDecodeBufLargeGarbage(t *testing.T) {
	d := NewDecoder()
	// Large garbage data — should not panic
	garbage := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 1000)
	markers := d.DecodeBuf(garbage)
	_ = markers // no panic = pass
}
