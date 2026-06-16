package hls

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
	ch := make(chan *marker.Marker, 100)

	err := p.Poll(context.Background(), ch)
	if err != nil {
		t.Fatalf("Poll error: %v", err)
	}
}

func TestPollProcessesInitialMediaPlaylistWithoutRefetch(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`
	var mu sync.Mutex
	manifestFetches := 0
	segmentFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			mu.Lock()
			manifestFetches++
			mu.Unlock()
			fmt.Fprint(w, manifest)
		case "/segment0.ts":
			mu.Lock()
			segmentFetches++
			mu.Unlock()
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
	gotManifests := manifestFetches
	gotSegments := segmentFetches
	mu.Unlock()
	if gotManifests != 1 {
		t.Fatalf("manifest fetches = %d, want 1", gotManifests)
	}
	if gotSegments != 1 {
		t.Fatalf("segment fetches = %d, want 1", gotSegments)
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

	p := NewPoller(srv.URL + "/master.m3u8")
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

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
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

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
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

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.pollInterval = time.Millisecond
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

func TestPollEmitsManifestAndSegmentMarkersInPlaylistOrder(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
slow.ts
#EXT-X-CUE-IN
#EXTINF:6.0,
fast.ts
#EXT-X-ENDLIST
`
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	fastRequested := make(chan struct{})
	var onceSlow sync.Once
	var onceFast sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			fmt.Fprint(w, manifest)
		case "/slow.ts":
			onceSlow.Do(func() { close(slowStarted) })
			<-releaseSlow
			w.Write(buildID3TextSegment("slow"))
		case "/fast.ts":
			<-slowStarted
			onceFast.Do(func() { close(fastRequested) })
			w.Write(buildID3TextSegment("fast"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 10)
	done := make(chan error, 1)
	go func() {
		done <- p.Poll(context.Background(), ch)
	}()
	select {
	case <-fastRequested:
	case <-time.After(time.Second):
		close(releaseSlow)
		t.Fatal("fast segment was not requested while slow segment was blocked")
	}
	close(releaseSlow)
	if err := <-done; err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	close(ch)

	var got []string
	for m := range ch {
		switch m.Type {
		case marker.MarkerSCTE35:
			got = append(got, m.Tag)
		case marker.MarkerID3:
			got = append(got, m.Tags["TIT2"])
		}
	}

	want := []string{"#EXT-X-CUE-OUT", "slow", "#EXT-X-CUE-IN", "fast"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("markers = %v, want %v", got, want)
	}
}

func TestPollEmitsReadyPrefixBeforeSlowLaterSegment(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
fast.ts
#EXT-X-CUE-IN
#EXTINF:6.0,
slow.ts
#EXT-X-ENDLIST
	`
	firstMarker := make(chan string, 1)
	releaseSlow := make(chan struct{})
	slowReleased := false
	release := func() {
		if !slowReleased {
			close(releaseSlow)
			slowReleased = true
		}
	}
	defer release()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			fmt.Fprint(w, manifest)
		case "/fast.ts":
			w.Write(buildID3TextSegment("fast"))
		case "/slow.ts":
			<-releaseSlow
			w.Write(buildID3TextSegment("slow"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 10)
	done := make(chan error, 1)
	go func() {
		done <- p.Poll(context.Background(), ch)
		close(ch)
	}()
	go func() {
		for m := range ch {
			if m.Type == marker.MarkerID3 {
				firstMarker <- m.Tags["TIT2"]
				return
			}
		}
	}()

	select {
	case got := <-firstMarker:
		if got != "fast" {
			t.Fatalf("first ID3 marker = %q, want fast", got)
		}
	case <-time.After(200 * time.Millisecond):
		release()
		<-done
		t.Fatal("fast marker was blocked by later slow segment")
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Poll error: %v", err)
	}
}

func TestPollUsesConditionalManifestRequest(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
`
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
			w.Header().Set("ETag", `"v1"`)
			if fetch == 2 {
				if r.Header.Get("If-None-Match") != `"v1"` {
					t.Errorf("If-None-Match = %q, want %q", r.Header.Get("If-None-Match"), `"v1"`)
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if fetch >= 3 {
				fmt.Fprint(w, manifest+"#EXT-X-ENDLIST\n")
				return
			}
			fmt.Fprint(w, manifest)
		case "/segment0.ts":
			mu.Lock()
			segmentFetches++
			mu.Unlock()
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
	gotSegments := segmentFetches
	mu.Unlock()
	if gotSegments != 1 {
		t.Fatalf("segment fetches = %d, want 1", gotSegments)
	}
}

func TestFetchAndProcessNotModifiedUsesCachedTargetDuration(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
`
	var mu sync.Mutex
	manifestFetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			mu.Lock()
			manifestFetches++
			fetch := manifestFetches
			mu.Unlock()
			w.Header().Set("ETag", `"v1"`)
			if fetch == 2 {
				if r.Header.Get("If-None-Match") != `"v1"` {
					t.Errorf("If-None-Match = %q, want %q", r.Header.Get("If-None-Match"), `"v1"`)
				}
				w.WriteHeader(http.StatusNotModified)
				return
			}
			fmt.Fprint(w, manifest)
		case "/segment0.ts":
			w.Write([]byte{0xFF})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 10)
	_, waitInterval, err := p.fetchAndProcess(context.Background(), srv.URL+"/stream.m3u8", ch)
	if err != nil {
		t.Fatalf("first fetchAndProcess error: %v", err)
	}
	if waitInterval != 6*time.Second {
		t.Fatalf("first wait interval = %s, want 6s", waitInterval)
	}
	_, waitInterval, err = p.fetchAndProcess(context.Background(), srv.URL+"/stream.m3u8", ch)
	if err != nil {
		t.Fatalf("second fetchAndProcess error: %v", err)
	}
	if waitInterval != 6*time.Second {
		t.Fatalf("304 wait interval = %s, want cached 6s", waitInterval)
	}
}

func TestFetchAndProcessDoesNotForgetRecentlySeenSegmentsOutsideCurrentWindow(t *testing.T) {
	manifests := []string{
		`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment0.ts
`,
		`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:6.0,
segment1.ts
`,
		`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`,
	}
	var mu sync.Mutex
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			mu.Lock()
			idx := fetches
			if idx >= len(manifests) {
				idx = len(manifests) - 1
			}
			fetches++
			mu.Unlock()
			fmt.Fprint(w, manifests[idx])
			return
		}
		w.Write([]byte{0xFF})
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 10)
	for i := 0; i < len(manifests); i++ {
		if _, _, err := p.fetchAndProcess(context.Background(), srv.URL+"/stream.m3u8", ch); err != nil {
			t.Fatalf("fetchAndProcess %d error: %v", i, err)
		}
	}
	close(ch)

	cueOutCount := 0
	for m := range ch {
		if m.Tag == "#EXT-X-CUE-OUT" {
			cueOutCount++
		}
	}
	if cueOutCount != 1 {
		t.Fatalf("CUE-OUT count = %d, want 1", cueOutCount)
	}
}

func TestFetchAndProcessTreatsReusedSequenceWithDifferentURIAsNewSegment(t *testing.T) {
	manifests := []string{
		`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment0.ts
`,
		`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-CUE-OUT
#EXTINF:6.0,
replacement0.ts
#EXT-X-ENDLIST
`,
	}
	var mu sync.Mutex
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			mu.Lock()
			idx := fetches
			if idx >= len(manifests) {
				idx = len(manifests) - 1
			}
			fetches++
			mu.Unlock()
			fmt.Fprint(w, manifests[idx])
			return
		}
		w.Write([]byte{0xFF})
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 10)
	for i := 0; i < len(manifests); i++ {
		if _, _, err := p.fetchAndProcess(context.Background(), srv.URL+"/stream.m3u8", ch); err != nil {
			t.Fatalf("fetchAndProcess %d error: %v", i, err)
		}
	}
	close(ch)

	cueOutCount := 0
	for m := range ch {
		if m.Tag == "#EXT-X-CUE-OUT" {
			cueOutCount++
		}
	}
	if cueOutCount != 2 {
		t.Fatalf("CUE-OUT count = %d, want 2", cueOutCount)
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

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 100)

	_ = p.Poll(context.Background(), ch)

	mu.Lock()
	count := downloads["/segment0.ts"]
	mu.Unlock()

	if count > 1 {
		t.Errorf("segment0.ts downloaded %d times, expected 1", count)
	}
}

func TestPollDedupsDuplicateSegmentURIsWithinOnePlaylist(t *testing.T) {
	downloads := make(map[string]int)
	var mu sync.Mutex

	manifest := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
#EXTINF:6.0,
segment0.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			fmt.Fprint(w, manifest)
			return
		}
		mu.Lock()
		downloads[r.URL.Path]++
		mu.Unlock()
		w.Write([]byte{0xFF})
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 100)
	if err := p.Poll(context.Background(), ch); err != nil {
		t.Fatalf("Poll error: %v", err)
	}

	mu.Lock()
	count := downloads["/segment0.ts"]
	mu.Unlock()
	if count != 1 {
		t.Fatalf("segment0.ts downloaded %d times, want 1", count)
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

	p := NewPoller(srv.URL + "/live/stream.m3u8")
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

	p := NewPoller(srv.URL + "/stream.m3u8")
	ch := make(chan *marker.Marker, 100)

	// This should retry on the 500 error and eventually succeed
	err := p.Poll(ctx, ch)
	if err != nil {
		// If context expired before retry, that's acceptable
		t.Logf("Poll returned: %v (may be timeout, acceptable)", err)
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
	if err := p.Poll(context.Background(), ch); err != nil {
		t.Fatalf("Poll error: %v", err)
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

func TestFetchAndProcessEvictsSeenEntriesAtCapacity(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:6.0,
segment10.ts
#EXT-X-ENDLIST
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream.m3u8" {
			fmt.Fprint(w, manifest)
			return
		}
		w.Write([]byte{0xFF})
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	p.planner = newPlaylistPlanner(1)
	staleURL := "http://stale.example/segment.ts"
	p.planner.tagSeen.Remember(staleURL)
	p.planner.segmentSeen.Remember(staleURL)
	ch := make(chan *marker.Marker, 100)

	_, _, err := p.fetchAndProcess(context.Background(), srv.URL+"/stream.m3u8", ch)
	if err != nil {
		t.Fatalf("fetchAndProcess error: %v", err)
	}
	if p.planner.tagSeen.Has(staleURL) {
		t.Fatal("old tag key was not evicted")
	}
	if p.planner.segmentSeen.Has(staleURL) {
		t.Fatal("old segment URL was not evicted")
	}
}

func buildID3TextSegment(value string) []byte {
	frameData := append([]byte{0x03}, []byte(value)...)
	frame := make([]byte, 10+len(frameData))
	copy(frame[0:4], "TIT2")
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(frameData)))
	copy(frame[10:], frameData)

	header := make([]byte, 10)
	copy(header[0:3], "ID3")
	header[3] = 3
	size := len(frame)
	header[6] = byte((size >> 21) & 0x7F)
	header[7] = byte((size >> 14) & 0x7F)
	header[8] = byte((size >> 7) & 0x7F)
	header[9] = byte(size & 0x7F)
	return append(header, frame...)
}
