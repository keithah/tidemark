package udp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/mpegts"
	"github.com/keithah/tidemark/internal/pipeline"
)

const udpReadBufferBytes = 2 << 20

// Reader reads MPEGTS data from UDP multicast and decodes SCTE-35.
type Reader struct {
	addr    string
	decoder *mpegts.Decoder
}

// NewReader creates a new UDP multicast reader.
func NewReader(addr string) *Reader {
	return &Reader{
		addr:    addr,
		decoder: mpegts.NewDecoder(),
	}
}

// Read opens a UDP multicast socket and emits markers on the channel.
// Blocks until context is cancelled.
func (r *Reader) Read(ctx context.Context, ch chan<- *marker.Marker) error {
	host, port, err := parseUDPAddr(r.addr)
	if err != nil {
		return err
	}

	group := net.ParseIP(host)
	if group == nil {
		return fmt.Errorf("invalid multicast group: %s", host)
	}

	addr := &net.UDPAddr{IP: group, Port: port}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("listen multicast: %w", err)
	}
	if err := conn.SetReadBuffer(udpReadBufferBytes); err != nil {
		_ = conn.Close()
		return fmt.Errorf("set read buffer: %w", err)
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer func() {
		close(done)
		_ = conn.Close()
	}()

	buf := make([]byte, 1316) // 7 * 188 bytes (standard MPEGTS datagram size)

	for {
		n, err := conn.Read(buf)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, net.ErrClosed) {
				return ctxErr
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Timeout — check ctx and retry
			}
			return fmt.Errorf("read: %w", err)
		}

		markers, err := r.decoder.DecodeBuf(buf[:n])
		if err != nil {
			return err
		}
		for _, m := range markers {
			m.Source = "udp_multicast"
			m.Timestamp = time.Now()
			if err := pipeline.SendMarker(ctx, ch, m); err != nil {
				return err
			}
		}
	}
}

func parseUDPAddr(addr string) (string, int, error) {
	// Remove udp:// prefix and optional @ sign
	addr = strings.TrimPrefix(addr, "udp://")
	addr = strings.TrimPrefix(addr, "@")

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("parse address %q: %w", addr, err)
	}

	port := 0
	_, err = fmt.Sscanf(portStr, "%d", &port)
	if err != nil {
		return "", 0, fmt.Errorf("parse port %q: %w", portStr, err)
	}

	return host, port, nil
}
