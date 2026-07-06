package hls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keithah/tidemark/internal/marker"
)

func TestPollFetchRetry(t *testing.T) {
	var mu sync.Mutex
	fetchCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchCount++
		fc := fetchCount
		mu.Unlock()

		if fc <= 2 {
			w.WriteHeader(500)
			return
		}
		manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`
		fmt.Fprint(w, manifest)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
	ch := make(chan *marker.Marker, 100)

	if err := p.Poll(ctx, ch); err != nil {
		t.Fatalf("Poll error: %v", err)
	}
}

func TestPollStopsAfterMaxConsecutiveFailures(t *testing.T) {
	var mu sync.Mutex
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetches++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", WithMaxConsecutiveFailures(2))
	p.pollInterval = time.Millisecond
	ch := make(chan *marker.Marker, 1)
	err := p.Poll(context.Background(), ch)
	if err == nil {
		t.Fatal("expected retry budget error")
	}
	if !strings.Contains(err.Error(), "giving up after 2 consecutive failures") {
		t.Fatalf("Poll error = %q, want retry budget message", err.Error())
	}
	mu.Lock()
	got := fetches
	mu.Unlock()
	if got != 2 {
		t.Fatalf("manifest fetches = %d, want 2", got)
	}
}

func TestPollFailsFastOnPermanentManifestStatus(t *testing.T) {
	var mu sync.Mutex
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetches++
		mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 1)
	err := p.Poll(context.Background(), ch)
	if err == nil {
		t.Fatal("expected permanent status error")
	}
	mu.Lock()
	got := fetches
	mu.Unlock()
	if got != 1 {
		t.Fatalf("manifest fetches = %d, want fail-fast single fetch", got)
	}
}

func TestPollReportsSegmentDownloadErrors(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
missing.ts
#EXT-X-ENDLIST
`
	var reported strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			fmt.Fprint(w, manifest)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", WithErrorWriter(&reported))
	ch := make(chan *marker.Marker, 100)
	err := p.Poll(context.Background(), ch)
	if err == nil {
		t.Fatal("expected Poll error for failed VOD segment decode")
	}
	if !strings.Contains(err.Error(), "decode segment") {
		t.Fatalf("Poll error = %q, want decode segment", err.Error())
	}
	if !strings.Contains(reported.String(), "decode segment") {
		t.Fatalf("reported errors = %q, want decode segment error", reported.String())
	}
}

func TestPollRetriesFailedSegmentOnNextPlaylist(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
`
	manifestEnd := manifest + "#EXT-X-ENDLIST\n"

	var mu sync.Mutex
	manifestFetches := 0
	segmentFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			mu.Lock()
			manifestFetches++
			fetch := manifestFetches
			mu.Unlock()
			if fetch <= 2 {
				fmt.Fprint(w, manifest)
				return
			}
			fmt.Fprint(w, manifestEnd)
		case "/segment0.ts":
			mu.Lock()
			segmentFetches++
			fetch := segmentFetches
			mu.Unlock()
			if fetch == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Write([]byte{0xFF})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
	ch := make(chan *marker.Marker, 100)
	if err := p.Poll(context.Background(), ch); err != nil {
		t.Fatalf("Poll error: %v", err)
	}

	mu.Lock()
	got := segmentFetches
	mu.Unlock()
	if got != 2 {
		t.Fatalf("segment fetches = %d, want retry on second playlist", got)
	}
}
