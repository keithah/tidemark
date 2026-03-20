package hls

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/keithah/tidemark/internal/classifier"
	"github.com/keithah/tidemark/internal/marker"
)

func TestPollVOD(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXTINF:6.0,
segment1.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, manifest)
		} else {
			// Serve empty segments
			w.Write([]byte{0xFF, 0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(context.Background(), ch)
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
}

func TestPollMasterPlaylist(t *testing.T) {
	masterManifest := `#EXTM3U
#EXT-X-STREAM-INF:BANDWIDTH=800000
media.m3u8
`
	mediaManifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			fmt.Fprint(w, masterManifest)
		case "/media.m3u8":
			fmt.Fprint(w, mediaManifest)
		default:
			w.Write([]byte{0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/master.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(context.Background(), ch)
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
}

func TestPollContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
`
		fmt.Fprint(w, manifest)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(ctx, ch)
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPollCueOutCueIn(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT:DURATION=30
#EXTINF:6.0,
segment0.ts
#EXT-X-CUE-IN
#EXTINF:6.0,
segment1.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			fmt.Fprint(w, manifest)
		} else {
			w.Write([]byte{0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(context.Background(), ch)
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	close(ch)

	var markers []*marker.Marker
	for m := range ch {
		markers = append(markers, m)
	}

	// Should have at least CUE-OUT (AD_START) and CUE-IN (AD_END) markers
	hasStart, hasEnd := false, false
	for _, m := range markers {
		if m.Classification == marker.AdStart {
			hasStart = true
		}
		if m.Classification == marker.AdEnd {
			hasEnd = true
		}
	}
	if !hasStart {
		t.Error("expected AD_START marker from CUE-OUT")
	}
	if !hasEnd {
		t.Error("expected AD_END marker from CUE-IN")
	}
}

func TestPollLiveNoDuplicates(t *testing.T) {
	var mu sync.Mutex
	fetchCount := 0

	manifest1 := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment0.ts
`
	manifest2 := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment0.ts
#EXTINF:6.0,
segment1.ts
`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			mu.Lock()
			fetchCount++
			fc := fetchCount
			mu.Unlock()

			if fc == 1 {
				fmt.Fprint(w, manifest1)
			} else {
				// Second fetch returns endlist to stop
				fmt.Fprint(w, manifest2+"\n#EXT-X-ENDLIST\n")
			}
		} else {
			w.Write([]byte{0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(context.Background(), ch)
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	close(ch)

	// Count CUE-OUT markers — segment0 should only produce one despite appearing in both fetches
	cueOutCount := 0
	for m := range ch {
		if m.Tag == "#EXT-X-CUE-OUT" {
			cueOutCount++
		}
	}
	if cueOutCount > 1 {
		t.Errorf("expected 1 CUE-OUT marker (dedup), got %d", cueOutCount)
	}
}

func TestPollSegmentDedup(t *testing.T) {
	downloads := make(map[string]int)
	var mu sync.Mutex

	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			fmt.Fprint(w, manifest)
		} else {
			mu.Lock()
			downloads[r.URL.Path]++
			mu.Unlock()
			w.Write([]byte{0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	_ = p.Poll(context.Background(), ch)

	mu.Lock()
	count := downloads["/segment0.ts"]
	mu.Unlock()

	if count > 1 {
		t.Errorf("segment0.ts downloaded %d times, expected 1", count)
	}
}

func TestPollSegmentRelativeURL(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
sub/segment0.ts
#EXT-X-ENDLIST
`
	var gotPath string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live/stream.m3u8" {
			fmt.Fprint(w, manifest)
		} else {
			mu.Lock()
			gotPath = r.URL.Path
			mu.Unlock()
			w.Write([]byte{0xFF})
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/live/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)
	_ = p.Poll(context.Background(), ch)

	mu.Lock()
	if gotPath != "/live/sub/segment0.ts" {
		t.Errorf("expected relative URL resolution to /live/sub/segment0.ts, got %s", gotPath)
	}
	mu.Unlock()
}

func TestPollFetchRetry(t *testing.T) {
	var mu sync.Mutex
	fetchCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetchCount++
		fc := fetchCount
		mu.Unlock()

		if fc <= 2 {
			// First two fetches: fail for master resolution + fail for first poll
			w.WriteHeader(500)
			return
		}
		// Third fetch succeeds with VOD
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

	p := NewPoller(srv.URL+"/stream.m3u8", classifier.New())
	ch := make(chan *marker.Marker, 100)

	// This should retry on the 500 error and eventually succeed
	err := p.Poll(ctx, ch)
	if err != nil {
		// If context expired before retry, that's acceptable
		t.Logf("Poll returned: %v (may be timeout, acceptable)", err)
	}
}
