package httpclient

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestTransportEnablesHTTP2AndConnectionPooling(t *testing.T) {
	tr := NewTransport()
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = false, want true")
	}
	if tr.MaxIdleConns < 16 {
		t.Fatalf("MaxIdleConns = %d, want at least 16", tr.MaxIdleConns)
	}
	if tr.MaxIdleConnsPerHost < 4 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want at least 4", tr.MaxIdleConnsPerHost)
	}
	if tr.MaxConnsPerHost < 4 {
		t.Fatalf("MaxConnsPerHost = %d, want at least 4", tr.MaxConnsPerHost)
	}
}

func TestWithIdleReadTimeoutClosesStalledBody(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()

	body := WithIdleReadTimeout(pr, 10*time.Millisecond)
	defer body.Close()

	_, err := body.Read(make([]byte, 1))
	if !errors.Is(err, ErrIdleReadTimeout) {
		t.Fatalf("Read error = %v, want %v", err, ErrIdleReadTimeout)
	}
}

func TestWithIdleReadTimeoutReusesTimerAcrossReads(t *testing.T) {
	body := WithIdleReadTimeout(io.NopCloser(strings.NewReader("abc")), time.Second)
	defer body.Close()

	wrapped, ok := body.(*idleReadCloser)
	if !ok {
		t.Fatalf("body = %T, want *idleReadCloser", body)
	}

	buf := make([]byte, 1)
	if _, err := body.Read(buf); err != nil {
		t.Fatalf("first read: %v", err)
	}
	first := wrapped.timer
	if _, err := body.Read(buf); err != nil {
		t.Fatalf("second read: %v", err)
	}
	if wrapped.timer != first {
		t.Fatal("idle timer was replaced instead of reset")
	}
}
