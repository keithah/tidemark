package httpclient

import (
	"errors"
	"io"
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
