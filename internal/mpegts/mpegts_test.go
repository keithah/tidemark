package mpegts

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

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
	markers, err := d.DecodeBuf(nil)
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for nil input, got %d", len(markers))
	}
}

func TestDecodeBufEmptySlice(t *testing.T) {
	d := NewDecoder()
	markers, err := d.DecodeBuf([]byte{})
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for empty input, got %d", len(markers))
	}
}

func TestDecodeBufReportsDecoderPanic(t *testing.T) {
	d := &Decoder{}
	markers, err := d.DecodeBuf([]byte{0x47})
	if err == nil {
		t.Fatal("expected decoder panic error")
	}
	if markers != nil {
		t.Fatalf("markers = %v, want nil on decoder panic", markers)
	}
}

func TestDecodeBufNoSCTE35(t *testing.T) {
	d := NewDecoder()
	// Valid-ish MPEGTS sync bytes but no SCTE-35
	data := bytes.Repeat([]byte{0x47, 0x00, 0x00, 0x10}, 47) // 188 bytes = 1 packet
	markers, err := d.DecodeBuf(data)
	if err != nil {
		t.Fatalf("DecodeBuf: %v", err)
	}
	if len(markers) != 0 {
		t.Errorf("expected 0 markers for non-SCTE35 data, got %d", len(markers))
	}
}

func TestDecodeReaderContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
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

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("DecodeReader error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("DecodeReader did not unblock after context cancellation")
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

func TestDecodeBufLargeGarbage(t *testing.T) {
	d := NewDecoder()
	// Large garbage data — should not panic
	garbage := bytes.Repeat([]byte{0xDE, 0xAD, 0xBE, 0xEF}, 1000)
	markers, _ := d.DecodeBuf(garbage)
	_ = markers // no panic = pass
}
