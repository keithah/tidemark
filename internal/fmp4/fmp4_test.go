package fmp4

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func TestParseInitExtractsTrackTimescale(t *testing.T) {
	init := box("moov",
		box("trak",
			box("tkhd", full(0, u32(0), u32(0), u32(7))),
			box("mdia",
				box("mdhd", full(0, u32(0), u32(0), u32(48000), u32(0), u16(0))),
				box("hdlr", full(0, u32(0), []byte("soun"), u32(0), u32(0), u32(0))),
			),
		),
	)

	info, err := ParseInit(init)
	if err != nil {
		t.Fatalf("ParseInit error = %v", err)
	}
	track, ok := info.Tracks[7]
	if !ok {
		t.Fatalf("track 7 missing from %#v", info.Tracks)
	}
	if track.Timescale != 48000 {
		t.Fatalf("timescale = %d, want 48000", track.Timescale)
	}
	if track.Handler != "soun" {
		t.Fatalf("handler = %q, want soun", track.Handler)
	}
}

func TestParseFragmentComputesSampleTimingFromTfdtAndTrun(t *testing.T) {
	init := InitInfo{Tracks: map[uint32]TrackInfo{
		7: {ID: 7, Timescale: 48000, Handler: "soun"},
	}}
	fragment := box("moof",
		box("traf",
			box("tfhd", full(0, u32(7))),
			box("tfdt", full(0x01000000, u64(96000))),
			box("trun", full(0x000100, u32(2), u32(1024), u32(2048))),
		),
	)

	timing, err := ParseFragment(init, fragment)
	if err != nil {
		t.Fatalf("ParseFragment error = %v", err)
	}
	if len(timing.Samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(timing.Samples))
	}
	first := timing.Samples[0]
	if first.TrackID != 7 || first.DecodeTime != 96000 || first.Duration != 1024 {
		t.Fatalf("first sample = %#v", first)
	}
	if first.StartSeconds != 2.0 {
		t.Fatalf("first start seconds = %f, want 2", first.StartSeconds)
	}
	second := timing.Samples[1]
	if second.DecodeTime != 97024 {
		t.Fatalf("second decode time = %d, want 97024", second.DecodeTime)
	}
	if second.StartSeconds != float64(97024)/48000 {
		t.Fatalf("second start seconds = %f", second.StartSeconds)
	}
}

func TestParseFragmentUsesTFHDDefaultSampleDuration(t *testing.T) {
	init := InitInfo{Tracks: map[uint32]TrackInfo{
		7: {ID: 7, Timescale: 48000, Handler: "soun"},
	}}
	fragment := box("moof",
		box("traf",
			box("tfhd", full(0x000008, u32(7), u32(1024))),
			box("tfdt", full(0x01000000, u64(96000))),
			box("trun", full(0, u32(2))),
		),
	)

	timing, err := ParseFragment(init, fragment)
	if err != nil {
		t.Fatalf("ParseFragment error = %v", err)
	}
	if len(timing.Samples) != 2 {
		t.Fatalf("samples = %d, want 2", len(timing.Samples))
	}
	if timing.Samples[0].Duration != 1024 || timing.Samples[1].DecodeTime != 97024 {
		t.Fatalf("samples = %#v", timing.Samples)
	}
}

func TestParseFragmentExtractsVersionOneEventMessage(t *testing.T) {
	fragment := box("emsg",
		full(0x01000000,
			u32(1000),
			u64(12345),
			u32(5000),
			u32(42),
			append(append([]byte("urn:test\x00"), []byte("value\x00")...), []byte("payload")...),
		),
	)

	timing, err := ParseFragment(InitInfo{}, fragment)
	if err != nil {
		t.Fatalf("ParseFragment error = %v", err)
	}
	if len(timing.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(timing.Events))
	}
	event := timing.Events[0]
	if event.SchemeIDURI != "urn:test" || event.Value != "value" || event.ID != 42 {
		t.Fatalf("event = %#v", event)
	}
	if event.PresentationTimeSeconds != 12.345 {
		t.Fatalf("presentation seconds = %f, want 12.345", event.PresentationTimeSeconds)
	}
	if string(event.MessageData) != "payload" {
		t.Fatalf("message data = %q, want payload", string(event.MessageData))
	}
}

func TestParseFragmentReaderSkipsLargeUnneededBox(t *testing.T) {
	init := InitInfo{Tracks: map[uint32]TrackInfo{
		7: {ID: 7, Timescale: 48000, Handler: "soun"},
	}}
	fragment := append(
		box("moof",
			box("traf",
				box("tfhd", full(0, u32(7))),
				box("tfdt", full(0x01000000, u64(96000))),
				box("trun", full(0x000100, u32(1), u32(1024))),
			),
		),
		box("mdat", bytes.Repeat([]byte{0xff}, 1<<20))...,
	)

	timing, err := ParseFragmentReader(init, bytes.NewReader(fragment), len(fragment))
	if err != nil {
		t.Fatalf("ParseFragmentReader error = %v", err)
	}
	if len(timing.Samples) != 1 {
		t.Fatalf("samples = %d, want 1", len(timing.Samples))
	}
	if timing.Samples[0].DecodeTime != 96000 {
		t.Fatalf("decode time = %d, want 96000", timing.Samples[0].DecodeTime)
	}
}

func TestParseFragmentReaderRejectsOversizedFragment(t *testing.T) {
	_, err := ParseFragmentReader(InitInfo{}, bytes.NewReader(box("free", []byte("toolarge"))), 8)
	if err == nil {
		t.Fatal("expected oversized fragment error")
	}
}

func TestParseFragmentReaderPropagatesReadErrors(t *testing.T) {
	_, err := ParseFragmentReader(InitInfo{}, &partialErrReader{}, 1024)
	if err == nil {
		t.Fatal("expected read error")
	}
}

func TestWalkBoxesStopsWhenCallbackReturnsFalse(t *testing.T) {
	data := append(box("ftyp", []byte("isom")), box("moov")...)
	visited := 0
	walkBoxes(data, func(box boxHeader) bool {
		visited++
		return false
	})
	if visited != 1 {
		t.Fatalf("visited boxes = %d, want 1", visited)
	}
}

type partialErrReader struct {
	done bool
}

func (r *partialErrReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.ErrUnexpectedEOF
	}
	r.done = true
	copy(p, []byte{0, 0, 0, 8})
	return 4, nil
}

func box(kind string, payload ...[]byte) []byte {
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

func full(flags uint32, payload ...[]byte) []byte {
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

func u16(v uint16) []byte {
	out := make([]byte, 2)
	binary.BigEndian.PutUint16(out, v)
	return out
}

func u32(v uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, v)
	return out
}

func u64(v uint64) []byte {
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, v)
	return out
}

func TestParseFragmentRejectsHugeSampleCount(t *testing.T) {
	init := InitInfo{Tracks: map[uint32]TrackInfo{
		7: {ID: 7, Timescale: 48000, Handler: "soun"},
	}}

	// trun declares 0xFFFFFFFF samples with no per-sample durations, plus a tfhd
	// default duration. A naive make([]uint32, sampleCount) would attempt ~16GB;
	// the guard must refuse to synthesize durations from a count larger than the
	// fragment itself instead of allocating (or panicking).
	fragment := box("moof",
		box("traf",
			box("tfhd", full(0x000008, u32(7), u32(1024))),
			box("tfdt", full(0x01000000, u64(96000))),
			box("trun", full(0, u32(0xFFFFFFFF))),
		),
	)

	timing, err := ParseFragment(init, fragment)
	if err != nil {
		t.Fatalf("ParseFragment error = %v", err)
	}
	if len(timing.Samples) != 0 {
		t.Fatalf("samples = %d, want 0 (implausible sample count rejected)", len(timing.Samples))
	}
}

func TestParseFragmentBoundsPerSampleCapacity(t *testing.T) {
	init := InitInfo{Tracks: map[uint32]TrackInfo{
		7: {ID: 7, Timescale: 48000, Handler: "soun"},
	}}

	// Per-sample durations flag is set with a huge sampleCount, but the box only
	// carries one duration entry. The read loop is bounded by the box length and
	// the preallocated capacity must not be driven by the raw count.
	fragment := box("moof",
		box("traf",
			box("tfhd", full(0, u32(7))),
			box("tfdt", full(0x01000000, u64(0))),
			box("trun", full(0x000100, u32(0xFFFFFFFF), u32(1024))),
		),
	)

	timing, err := ParseFragment(init, fragment)
	if err != nil {
		t.Fatalf("ParseFragment error = %v", err)
	}
	if len(timing.Samples) != 1 {
		t.Fatalf("samples = %d, want 1 (bounded by box length)", len(timing.Samples))
	}
}
