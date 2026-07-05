package udp

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/keithah/tidemark/internal/marker"
)

type fakeUDPConn struct {
	closed chan struct{}
	once   sync.Once
}

func newFakeUDPConn() *fakeUDPConn {
	return &fakeUDPConn{closed: make(chan struct{})}
}

func (c *fakeUDPConn) Read([]byte) (int, error) {
	<-c.closed
	return 0, net.ErrClosed
}

func (c *fakeUDPConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeUDPConn) SetReadBuffer(int) error { return nil }

func TestParseUDPAddr(t *testing.T) {
	tests := []struct {
		input    string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"udp://@239.1.1.1:5000", "239.1.1.1", 5000, false},
		{"239.1.1.1:5000", "239.1.1.1", 5000, false},
		{"udp://239.1.1.1:1234", "239.1.1.1", 1234, false},
		{"bad-address", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			host, port, err := parseUDPAddr(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if host != tt.wantHost {
				t.Errorf("host = %q, want %q", host, tt.wantHost)
			}
			if port != tt.wantPort {
				t.Errorf("port = %d, want %d", port, tt.wantPort)
			}
		})
	}
}

func TestReadContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFakeUDPConn()
	r := NewReader("udp://@239.0.0.1:9999")
	r.listen = func(string, *net.Interface, *net.UDPAddr) (udpConn, error) {
		return fake, nil
	}
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- r.Read(ctx, ch)
	}()

	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read error = %v, want context.Canceled", err)
	}
}

func TestReadInvalidAddressIsPermanent(t *testing.T) {
	r := NewReader("bad-address")
	err := r.Read(context.Background(), make(chan *marker.Marker, 1))
	if err == nil {
		t.Fatal("expected invalid address error")
	}
	var perr interface{ Permanent() bool }
	if !errors.As(err, &perr) || !perr.Permanent() {
		t.Fatalf("Read error = %T, want permanent error", err)
	}
}

func TestNewReader(t *testing.T) {
	r := NewReader("udp://@239.1.1.1:5000")
	if r == nil {
		t.Fatal("NewReader returned nil")
	}
	if r.decoder == nil {
		t.Fatal("decoder is nil")
	}
}

func TestReadDoesNotCloseChannel(t *testing.T) {
	// Verify that Read does not close the channel — caller manages lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	fake := newFakeUDPConn()
	r := NewReader("udp://@239.0.0.1:9998")
	r.listen = func(string, *net.Interface, *net.UDPAddr) (udpConn, error) {
		return fake, nil
	}
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- r.Read(ctx, ch)
	}()

	cancel()
	<-done

	// Channel should still be open — writing should not panic
	ch <- &marker.Marker{}
	close(ch)
}
