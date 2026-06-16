package httpclient

import (
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	connectTimeout        = 10 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 10 * time.Second
	idleConnTimeout       = 90 * time.Second
	maxIdleConns          = 32
	maxIdleConnsPerHost   = 8
	maxConnsPerHost       = 16
	maxDrainBytes         = 64 << 10

	// DefaultIdleReadTimeout bounds how long a streaming response may stall between bytes.
	DefaultIdleReadTimeout = 30 * time.Second
)

var ErrIdleReadTimeout = errors.New("idle read timeout")

// NewStreaming returns a client suitable for long-lived streaming responses.
// It bounds connection setup and response headers without imposing a full-body timeout.
func NewStreaming() *http.Client {
	return NewStreamingWithTransport(NewTransport())
}

// NewStreamingWithTransport returns a streaming client using the provided transport.
func NewStreamingWithTransport(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport}
}

// NewTimed returns a client with both setup/header bounds and an overall request timeout.
func NewTimed(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: NewTransport(),
	}
}

func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   connectTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		MaxConnsPerHost:       maxConnsPerHost,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       idleConnTimeout,
	}
}

// DrainAndClose drains a bounded response body prefix before closing it.
func DrainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrainBytes))
	_ = body.Close()
}

// WithIdleReadTimeout closes body if no bytes are read before timeout.
func WithIdleReadTimeout(body io.ReadCloser, timeout time.Duration) io.ReadCloser {
	if body == nil || timeout <= 0 {
		return body
	}
	w := &idleReadCloser{
		body:    body,
		timeout: timeout,
	}
	w.scheduleTimerLocked()
	return w
}

type idleReadCloser struct {
	body    io.ReadCloser
	timeout time.Duration
	timer   *time.Timer

	mu       sync.Mutex
	closed   bool
	timedOut bool
	timerSeq int
}

func (r *idleReadCloser) Read(p []byte) (int, error) {
	n, err := r.body.Read(p)
	if n > 0 {
		r.resetTimer()
	}
	if err != nil && r.isTimedOut() {
		return n, ErrIdleReadTimeout
	}
	return n, err
}

func (r *idleReadCloser) Close() error {
	r.mu.Lock()
	r.closed = true
	if r.timer != nil {
		r.timer.Stop()
	}
	r.mu.Unlock()
	return r.body.Close()
}

func (r *idleReadCloser) timeoutBody(seq int) {
	r.mu.Lock()
	if r.closed || seq != r.timerSeq {
		r.mu.Unlock()
		return
	}
	r.timedOut = true
	r.mu.Unlock()
	_ = r.body.Close()
}

func (r *idleReadCloser) resetTimer() {
	r.mu.Lock()
	if r.closed || r.timedOut {
		r.mu.Unlock()
		return
	}
	if r.timer != nil {
		r.timer.Stop()
	}
	r.scheduleTimerLocked()
	r.mu.Unlock()
}

func (r *idleReadCloser) scheduleTimerLocked() {
	r.timerSeq++
	seq := r.timerSeq
	r.timer = time.AfterFunc(r.timeout, func() {
		r.timeoutBody(seq)
	})
}

func (r *idleReadCloser) isTimedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.timedOut
}
