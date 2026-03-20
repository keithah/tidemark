package detector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/keithah/tidemark/internal/marker"
)

func TestDetectHLSByExtension(t *testing.T) {
	result, err := Detect(context.Background(), "https://example.com/live/stream.m3u8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamHLS {
		t.Errorf("expected HLS, got %s", result.Type)
	}
}

func TestDetectHLSByExtensionWithQuery(t *testing.T) {
	result, err := Detect(context.Background(), "https://example.com/live/stream.m3u8?token=abc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamHLS {
		t.Errorf("expected HLS, got %s", result.Type)
	}
}

func TestDetectHLSByPath(t *testing.T) {
	result, err := Detect(context.Background(), "https://example.com/hls/stream")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamHLS {
		t.Errorf("expected HLS, got %s", result.Type)
	}
}

func TestDetectMPEGTSByExtension(t *testing.T) {
	result, err := Detect(context.Background(), "https://example.com/video.ts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamMPEGTS {
		t.Errorf("expected MPEGTS, got %s", result.Type)
	}
}

func TestDetectICYByHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("icy-metaint", "16000")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result, err := Detect(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamICY {
		t.Errorf("expected ICY, got %s", result.Type)
	}
	if result.MetaInt != 16000 {
		t.Errorf("expected MetaInt 16000, got %d", result.MetaInt)
	}
}

func TestDetectICYFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result, err := Detect(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamICY {
		t.Errorf("expected ICY fallback, got %s", result.Type)
	}
}

func TestDetectUDP(t *testing.T) {
	result, err := Detect(context.Background(), "udp://@239.1.1.1:5000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamUDP {
		t.Errorf("expected UDP, got %s", result.Type)
	}
}

func TestDetectUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result, err := Detect(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Type != marker.StreamUnknown {
		t.Errorf("expected Unknown, got %s", result.Type)
	}
}

func TestDetectContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	_, err := Detect(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}
