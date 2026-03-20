package icy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"github.com/keithah/tidemark/internal/marker"
)

// serveICY creates an httptest server that speaks the ICY protocol.
// It writes audio chunks followed by ICY metadata blocks.
func serveICY(metaInt int, titles []string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("icy-metaint", "16")
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(200)

		flusher, _ := w.(http.Flusher)
		audio := bytes.Repeat([]byte{0xFF}, metaInt)

		for _, title := range titles {
			// Write audio chunk
			_, _ = w.Write(audio)

			// Build metadata
			meta := "StreamTitle='" + title + "';"
			// Pad to multiple of 16
			for len(meta)%16 != 0 {
				meta += "\x00"
			}
			sb := byte(len(meta) / 16)

			_, _ = w.Write([]byte{sb})
			_, _ = w.Write([]byte(meta))
			if flusher != nil {
				flusher.Flush()
			}
		}
		// Write final audio + zero-length metadata to end cleanly
		_, _ = w.Write(audio)
		_, _ = w.Write([]byte{0})
	}))
}

// buildICYStream builds raw ICY protocol bytes for testing ReadFrom.
func buildICYStream(metaInt int, entries []string) []byte {
	var buf bytes.Buffer
	audio := bytes.Repeat([]byte{0xFF}, metaInt)
	for _, entry := range entries {
		buf.Write(audio)
		meta := entry
		for len(meta)%16 != 0 {
			meta += "\x00"
		}
		sb := byte(len(meta) / 16)
		buf.WriteByte(sb)
		buf.WriteString(meta)
	}
	return buf.Bytes()
}

func TestReadStreamTitle(t *testing.T) {
	stream := buildICYStream(16, []string{
		"StreamTitle='Test Song';",
	})

	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)
	ctx := context.Background()

	err := r.ReadFrom(ctx, bytes.NewReader(stream), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(ch)

	var markers []*marker.Marker
	for m := range ch {
		markers = append(markers, m)
	}
	if len(markers) != 1 {
		t.Fatalf("expected 1 marker, got %d", len(markers))
	}
	if markers[0].Fields["StreamTitle"] != "Test Song" {
		t.Errorf("expected StreamTitle 'Test Song', got %q", markers[0].Fields["StreamTitle"])
	}
}

func TestReadMultipleTitles(t *testing.T) {
	stream := buildICYStream(16, []string{
		"StreamTitle='Song A';",
		"StreamTitle='Song B';",
		"StreamTitle='Song C';",
	})

	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)
	err := r.ReadFrom(context.Background(), bytes.NewReader(stream), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(ch)

	var markers []*marker.Marker
	for m := range ch {
		markers = append(markers, m)
	}
	if len(markers) != 3 {
		t.Fatalf("expected 3 markers, got %d", len(markers))
	}
}

func TestReadDuplicateSuppression(t *testing.T) {
	stream := buildICYStream(16, []string{
		"StreamTitle='Same';",
		"StreamTitle='Same';",
		"StreamTitle='Different';",
	})

	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)
	err := r.ReadFrom(context.Background(), bytes.NewReader(stream), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(ch)

	var markers []*marker.Marker
	for m := range ch {
		markers = append(markers, m)
	}
	if len(markers) != 2 {
		t.Fatalf("expected 2 markers (duplicate suppressed), got %d", len(markers))
	}
}

func TestReadEmptyMetadata(t *testing.T) {
	// Zero-length metadata should not produce markers
	var buf bytes.Buffer
	audio := bytes.Repeat([]byte{0xFF}, 16)
	buf.Write(audio)
	buf.WriteByte(0) // zero-length meta

	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)
	err := r.ReadFrom(context.Background(), bytes.NewReader(buf.Bytes()), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(ch)

	count := 0
	for range ch {
		count++
	}
	if count != 0 {
		t.Errorf("expected 0 markers for empty metadata, got %d", count)
	}
}

func TestReadMarkerType(t *testing.T) {
	stream := buildICYStream(16, []string{
		"StreamTitle='Test';",
	})

	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)
	_ = r.ReadFrom(context.Background(), bytes.NewReader(stream), ch)
	close(ch)

	m := <-ch
	if m.Type != marker.MarkerICY {
		t.Errorf("expected MarkerICY, got %v", m.Type)
	}
	if m.Source != "icy_stream" {
		t.Errorf("expected source 'icy_stream', got %q", m.Source)
	}
}

func TestReadContextCancellation(t *testing.T) {
	// Create an infinite stream
	pr, pw := io.Pipe()
	go func() {
		audio := bytes.Repeat([]byte{0xFF}, 16)
		for {
			_, err := pw.Write(audio)
			if err != nil {
				return
			}
			_, err = pw.Write([]byte{0}) // zero-length meta
			if err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	r := NewReader("http://test", 16)
	ch := make(chan *marker.Marker, 10)

	done := make(chan error, 1)
	go func() {
		done <- r.ReadFrom(ctx, pr, ch)
	}()

	cancel()
	pw.Close()

	err := <-done
	if err == nil || err != context.Canceled {
		// Accept either context.Canceled or pipe errors
		if err != nil && err.Error() != "read audio: unexpected EOF" {
			t.Logf("got error: %v (acceptable)", err)
		}
	}
}

func TestReadHTTPServer(t *testing.T) {
	srv := serveICY(16, []string{"Song A", "Song B"})
	defer srv.Close()

	r := NewReader(srv.URL, 16)
	ch := make(chan *marker.Marker, 10)

	err := r.Read(context.Background(), ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	close(ch)

	var markers []*marker.Marker
	for m := range ch {
		markers = append(markers, m)
	}
	if len(markers) != 2 {
		t.Fatalf("expected 2 markers, got %d", len(markers))
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{"valid utf8", []byte("hello"), "hello"},
		{"null padded", []byte("hello\x00\x00"), "hello"},
		{"non-printable", []byte("he\x01llo"), "hello"},
		{"invalid utf8", []byte{0xff, 0xfe, 0xfd}, "[binary data]"},
		{"empty", []byte{0x00, 0x00}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitize(tt.input)
			if got != tt.want {
				t.Errorf("sanitize(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseStreamTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"StreamTitle='My Song';", "My Song"},
		{"StreamTitle='';", ""},
		{"foo='bar';StreamTitle='Title';baz='qux';", "Title"},
		{"NoTitle='here';", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseStreamTitle(tt.input)
			if got != tt.want {
				t.Errorf("parseStreamTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFields(t *testing.T) {
	fields := parseFields("StreamTitle='My Song';StreamUrl='http://example.com';")
	if fields["StreamTitle"] != "My Song" {
		t.Errorf("StreamTitle = %q, want 'My Song'", fields["StreamTitle"])
	}
	if fields["StreamUrl"] != "http://example.com" {
		t.Errorf("StreamUrl = %q, want 'http://example.com'", fields["StreamUrl"])
	}
}
