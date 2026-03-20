package udp

import (
	"context"
	"net"
	"testing"
	"github.com/keithah/tidemark/internal/marker"
)

func tryMulticastListen(t *testing.T) {
	t.Helper()
	addr := &net.UDPAddr{IP: net.ParseIP("239.0.0.1"), Port: 0}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		t.Skipf("multicast not available: %v", err)
	}
	conn.Close()
}

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
	tryMulticastListen(t)

	ctx, cancel := context.WithCancel(context.Background())
	r := NewReader("udp://@239.0.0.1:9999")
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- r.Read(ctx, ch)
	}()

	cancel()
	err := <-done
	if err != nil && err != context.Canceled {
		t.Logf("got error: %v (acceptable)", err)
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
	tryMulticastListen(t)

	ctx, cancel := context.WithCancel(context.Background())
	r := NewReader("udp://@239.0.0.1:9998")
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
