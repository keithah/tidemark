package hls

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePlaylistGroupsPendingTagsWithSegments(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:42
#EXT-X-CUE-OUT
#EXTINF:6.0,
segment42.ts
#EXTINF:6.0,
segment43.ts
#EXT-X-ENDLIST
`
	playlist := ParsePlaylist(manifest)
	if !playlist.Endlist {
		t.Fatal("Endlist = false, want true")
	}
	if playlist.TargetDuration != 0 {
		t.Fatalf("target duration = %s, want 0", playlist.TargetDuration)
	}
	if len(playlist.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(playlist.Segments))
	}
	if playlist.Segments[0].Sequence != 42 {
		t.Fatalf("first sequence = %d, want 42", playlist.Segments[0].Sequence)
	}
	if playlist.Segments[0].URI != "segment42.ts" {
		t.Fatalf("first URI = %q, want segment42.ts", playlist.Segments[0].URI)
	}
	if len(playlist.Segments[0].Tags) != 1 || playlist.Segments[0].Tags[0].Tag != "#EXT-X-CUE-OUT" {
		t.Fatalf("first segment tags = %#v, want CUE-OUT", playlist.Segments[0].Tags)
	}
	if len(playlist.Segments[1].Tags) != 0 {
		t.Fatalf("second segment tags = %#v, want none", playlist.Segments[1].Tags)
	}
}

func TestPollIntervalUsesTargetDurationWhenDefault(t *testing.T) {
	playlist := ParsePlaylist(`#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.0,
segment0.ts
`)
	p := NewPoller("http://example.test/stream.m3u8")
	if got := p.pollIntervalFor(playlist); got != 6*time.Second {
		t.Fatalf("poll interval = %s, want 6s", got)
	}
	p.pollInterval = time.Millisecond
	if got := p.pollIntervalFor(playlist); got != time.Millisecond {
		t.Fatalf("override poll interval = %s, want 1ms", got)
	}
}

func TestFetchManifestRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", MaxManifestBytes+1)))
	}))
	defer srv.Close()

	p := NewPoller(srv.URL + "/stream.m3u8")
	_, err := p.fetchManifest(context.Background(), srv.URL+"/stream.m3u8")
	if err == nil {
		t.Fatal("expected oversized manifest error")
	}
	if !strings.Contains(err.Error(), "manifest too large") {
		t.Fatalf("error = %q, want manifest too large", err.Error())
	}
}
