package hls

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/keithah/tidemark/internal/marker"
)

func TestPollDecodesFMP4SegmentTimingFromMap(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-MAP:URI="map.mp4a"
#EXTINF:6.0,
segment0.mp4a
#EXT-X-ENDLIST
`
	init := fmp4InitSegmentForTest(7, 48000, "soun")
	fragment := append(
		fmp4EventForTest(1000, 12345, 5000, 42, "urn:test", "value", []byte("payload")),
		fmp4FragmentForTest(7, 96000, 1024, 1024, 2048)...,
	)

	var reported strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			fmt.Fprint(w, manifest)
		case "/map.mp4a":
			w.Write(init)
		case "/segment0.mp4a":
			w.Write(fragment)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", WithErrorWriter(&reported))
	ch := make(chan *marker.Marker, 10)
	if err := p.Poll(context.Background(), ch); err != nil {
		t.Fatalf("Poll error: %v", err)
	}
	if reported.String() != "" {
		t.Fatalf("reported errors = %q, want none", reported.String())
	}
	if len(ch) != 2 {
		t.Fatalf("markers = %d, want 2", len(ch))
	}
	m := <-ch
	if m.Type != marker.MarkerFMP4 {
		t.Fatalf("marker type = %s, want FMP4", m.Type)
	}
	if m.Tag != "timeline" {
		t.Fatalf("marker tag = %q, want timeline", m.Tag)
	}
	if m.PTS != 2.0 {
		t.Fatalf("marker PTS = %f, want 2", m.PTS)
	}
	if m.Fields["track_id"] != "7" || m.Fields["handler"] != "soun" {
		t.Fatalf("fields = %#v", m.Fields)
	}
	if m.Fields["sample_count"] != "3" || m.Fields["duration"] != "4096" {
		t.Fatalf("timeline fields = %#v", m.Fields)
	}
	event := <-ch
	if event.Tag != "emsg" {
		t.Fatalf("event tag = %q, want emsg", event.Tag)
	}
	if event.PTS != 12.345 {
		t.Fatalf("event PTS = %f, want 12.345", event.PTS)
	}
	if event.Fields["scheme_id_uri"] != "urn:test" || event.Fields["event_id"] != "42" {
		t.Fatalf("event fields = %#v", event.Fields)
	}
}

func TestPollRejectsOversizedFMP4InitSegment(t *testing.T) {
	manifest := `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-MAP:URI="map.mp4a"
#EXTINF:6.0,
segment0.mp4a
#EXT-X-ENDLIST
`
	var reported strings.Builder
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stream.m3u8":
			fmt.Fprint(w, manifest)
		case "/map.mp4a":
			w.Write(make([]byte, MaxInitSegmentBytes+1))
		case "/segment0.mp4a":
			w.Write(fmp4FragmentForTest(7, 0, 1024))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := NewPoller(srv.URL+"/stream.m3u8", WithErrorWriter(&reported))
	ch := make(chan *marker.Marker, 10)
	err := p.Poll(context.Background(), ch)
	if err == nil {
		t.Fatal("expected oversized init segment error")
	}
	if !strings.Contains(err.Error(), "fmp4 init too large") {
		t.Fatalf("Poll error = %q, want fmp4 init too large", err.Error())
	}
}

func fmp4InitSegmentForTest(trackID, timescale uint32, handler string) []byte {
	return mp4BoxForTest("moov",
		mp4BoxForTest("trak",
			mp4BoxForTest("tkhd", mp4FullBoxForTest(0, u32ForTest(0), u32ForTest(0), u32ForTest(trackID))),
			mp4BoxForTest("mdia",
				mp4BoxForTest("mdhd", mp4FullBoxForTest(0, u32ForTest(0), u32ForTest(0), u32ForTest(timescale), u32ForTest(0), u16ForTest(0))),
				mp4BoxForTest("hdlr", mp4FullBoxForTest(0, u32ForTest(0), []byte(handler), u32ForTest(0), u32ForTest(0), u32ForTest(0))),
			),
		),
	)
}

func fmp4FragmentForTest(trackID uint32, baseDecodeTime uint64, durations ...uint32) []byte {
	payload := [][]byte{u32ForTest(uint32(len(durations)))}
	for _, duration := range durations {
		payload = append(payload, u32ForTest(duration))
	}
	return mp4BoxForTest("moof",
		mp4BoxForTest("traf",
			mp4BoxForTest("tfhd", mp4FullBoxForTest(0, u32ForTest(trackID))),
			mp4BoxForTest("tfdt", mp4FullBoxForTest(0x01000000, u64ForTest(baseDecodeTime))),
			mp4BoxForTest("trun", mp4FullBoxForTest(0x000100, payload...)),
		),
	)
}

func fmp4EventForTest(timescale uint32, presentationTime uint64, duration, id uint32, scheme, value string, message []byte) []byte {
	payload := []byte{}
	payload = append(payload, u32ForTest(timescale)...)
	payload = append(payload, u64ForTest(presentationTime)...)
	payload = append(payload, u32ForTest(duration)...)
	payload = append(payload, u32ForTest(id)...)
	payload = append(payload, []byte(scheme)...)
	payload = append(payload, 0)
	payload = append(payload, []byte(value)...)
	payload = append(payload, 0)
	payload = append(payload, message...)
	return mp4BoxForTest("emsg", mp4FullBoxForTest(0x01000000, payload))
}

func mp4BoxForTest(kind string, payload ...[]byte) []byte {
	size := 8
	for _, p := range payload {
		size += len(p)
	}
	out := make([]byte, size)
	binary.BigEndian.PutUint32(out[:4], uint32(size))
	copy(out[4:8], kind)
	offset := 8
	for _, p := range payload {
		copy(out[offset:], p)
		offset += len(p)
	}
	return out
}

func mp4FullBoxForTest(flags uint32, payload ...[]byte) []byte {
	out := make([]byte, 4)
	out[0] = byte(flags >> 24)
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	for _, p := range payload {
		out = append(out, p...)
	}
	return out
}

func u16ForTest(v uint16) []byte {
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, v)
	return out
}

func u32ForTest(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

func u64ForTest(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}
