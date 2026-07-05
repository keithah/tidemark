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
	listen  listenMulticastFunc
}

type udpConn interface {
	Read([]byte) (int, error)
	Close() error
	SetReadBuffer(int) error
}

type listenMulticastFunc func(network string, ifi *net.Interface, gaddr *net.UDPAddr) (udpConn, error)

func listenMulticastUDP(network string, ifi *net.Interface, gaddr *net.UDPAddr) (udpConn, error) {
	return net.ListenMulticastUDP(network, ifi, gaddr)
}

// NewReader creates a new UDP multicast reader.
func NewReader(addr string) *Reader {
	return &Reader{
		addr:    addr,
		decoder: mpegts.NewDecoder(),
		listen:  listenMulticastUDP,
	}
}

// Read opens a UDP multicast socket and emits markers on the channel.
// Blocks until context is cancelled.
func (r *Reader) Read(ctx context.Context, ch chan<- *marker.Marker) error {
	host, port, err := parseUDPAddr(r.addr)
	if err != nil {
		return permanentError{err: err}
	}

	group := net.ParseIP(host)
	if group == nil {
		return permanentError{err: fmt.Errorf("invalid multicast group: %s", host)}
	}

	addr := &net.UDPAddr{IP: group, Port: port}
	conn, err := r.listen("udp4", nil, addr)
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

	// Sized to hold any UDP datagram (max ~65507 bytes). The common MPEG-TS
	// datagram is 7*188=1316 bytes, but larger senders exist; a 1316-byte buffer
	// would let the OS silently truncate them and corrupt packet alignment.
	buf := make([]byte, 65536)

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

type permanentError struct {
	err error
}

func (e permanentError) Error() string {
	return e.err.Error()
}

func (e permanentError) Unwrap() error {
	return e.err
}

func (e permanentError) Permanent() bool {
	return true
}
