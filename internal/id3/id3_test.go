package id3

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// buildFrame creates an ID3v2 frame with correct header.
func buildFrame(id string, version int, data []byte) []byte {
	frame := make([]byte, 10+len(data))
	copy(frame[0:4], id)

	if version == 4 {
		// Synchsafe size
		size := len(data)
		frame[4] = byte((size >> 21) & 0x7F)
		frame[5] = byte((size >> 14) & 0x7F)
		frame[6] = byte((size >> 7) & 0x7F)
		frame[7] = byte(size & 0x7F)
	} else {
		// Big-endian size
		binary.BigEndian.PutUint32(frame[4:8], uint32(len(data)))
	}

	// Flags = 0
	frame[8] = 0
	frame[9] = 0

	copy(frame[10:], data)
	return frame
}

// buildTag wraps frames into an ID3v2 tag with header.
func buildTag(version int, frames ...[]byte) []byte {
	var frameData []byte
	for _, f := range frames {
		frameData = append(frameData, f...)
	}

	header := make([]byte, 10)
	header[0] = 'I'
	header[1] = 'D'
	header[2] = '3'
	header[3] = byte(version) // major version
	header[4] = 0             // revision
	header[5] = 0             // flags

	// Synchsafe tag size
	size := len(frameData)
	header[6] = byte((size >> 21) & 0x7F)
	header[7] = byte((size >> 14) & 0x7F)
	header[8] = byte((size >> 7) & 0x7F)
	header[9] = byte(size & 0x7F)

	return append(header, frameData...)
}

// buildMPEGTSSegment wraps id3data in a minimal PES packet and packs it into
// 188-byte MPEGTS TS packets. The first packet has PUSI=1; subsequent packets
// have PUSI=0. The PES header is 9 bytes: start code (00 00 01), stream_id
// (0xFC = private), 2-byte length (0 = unbounded), and 3 optional header bytes
// (flags=0x80, no PTS/DTS, header_data_length=0).
func buildMPEGTSSegment(pid uint16, id3data []byte) []byte {
	// Build PES: 9-byte header + id3data
	pes := make([]byte, 9+len(id3data))
	copy(pes[:9], []byte{0x00, 0x00, 0x01, 0xFC, 0x00, 0x00, 0x80, 0x00, 0x00})
	copy(pes[9:], id3data)

	var result []byte
	remaining := pes
	first := true
	cc := byte(0)
	for len(remaining) > 0 {
		pkt := make([]byte, 188)
		pkt[0] = 0x47
		pkt[1] = byte(pid>>8) & 0x1F
		if first {
			pkt[1] |= 0x40 // PUSI
		}
		pkt[2] = byte(pid)
		pkt[3] = 0x10 | cc // payload only, no adaptation field
		n := copy(pkt[4:], remaining)
		remaining = remaining[n:]
		result = append(result, pkt...)
		first = false
		cc = (cc + 1) & 0x0F
	}
	return result
}

func TestParseTIT2v23(t *testing.T) {
	frame := buildFrame("TIT2", 3, []byte{0x00, 'H', 'e', 'l', 'l', 'o'}) // encoding=0 (ISO-8859-1)
	tag := buildTag(3, frame)
	tags, err := Parse(tag)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ID != "TIT2" || tags[0].Value != "Hello" {
		t.Errorf("got {%s, %q}, want {TIT2, Hello}", tags[0].ID, tags[0].Value)
	}
}

func TestParseTIT2v24(t *testing.T) {
	frame := buildFrame("TIT2", 4, []byte{0x03, 'W', 'o', 'r', 'l', 'd'}) // encoding=3 (UTF-8)
	tag := buildTag(4, frame)
	tags, err := Parse(tag)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "World" {
		t.Errorf("Value = %q, want 'World'", tags[0].Value)
	}
}

func TestParseTIT3(t *testing.T) {
	frame := buildFrame("TIT3", 3, []byte{0x00, 'S', 'u', 'b'})
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 || tags[0].ID != "TIT3" {
		t.Errorf("expected TIT3 tag")
	}
}

func TestParseTXXX(t *testing.T) {
	// description=null, value
	data := []byte{0x00, 0x00, 'v', 'a', 'l'}
	frame := buildFrame("TXXX", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "val" {
		t.Errorf("Value = %q, want 'val'", tags[0].Value)
	}
}

func TestParseTXXXWithDescription(t *testing.T) {
	data := []byte{0x00, 'd', 'e', 's', 'c', 0x00, 'v', 'a', 'l'}
	frame := buildFrame("TXXX", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "desc:val" {
		t.Errorf("Value = %q, want 'desc:val'", tags[0].Value)
	}
}

func TestParsePRIV(t *testing.T) {
	data := []byte{'o', 'w', 'n', 'e', 'r', 0x00, 0xDE, 0xAD}
	frame := buildFrame("PRIV", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if !strings.HasPrefix(tags[0].Value, "owner:") {
		t.Errorf("Value = %q, want prefix 'owner:'", tags[0].Value)
	}
}

func TestParseGEOB(t *testing.T) {
	// encoding=0, mime="text/plain\0", filename="f.txt\0", desc="d\0", data
	data := append([]byte{0x00},
		append([]byte("text/plain\x00"),
			append([]byte("f.txt\x00"),
				append([]byte("d\x00"),
					[]byte{0xCA, 0xFE}...)...)...)...)
	frame := buildFrame("GEOB", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if !strings.HasPrefix(tags[0].Value, "text/plain:") {
		t.Errorf("Value = %q, want prefix 'text/plain:'", tags[0].Value)
	}
}

func TestParseLINK(t *testing.T) {
	frame := buildFrame("LINK", 4, []byte{'W', 'X', 'X', 'X', 0x00, '4', '3', '0', '3', '3', '1', '1'})
	tag := buildTag(4, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ID != "LINK" || tags[0].Value != "4303311" {
		t.Errorf("got {%s, %q}, want {LINK, 4303311}", tags[0].ID, tags[0].Value)
	}
}

func TestParseWXXX(t *testing.T) {
	frame := buildFrame("WXXX", 4, []byte{0x03, 'i', 'd', 0x00, 'h', 't', 't', 'p', 's', ':', '/', '/', 'e', 'x'})
	tag := buildTag(4, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ID != "WXXX" || tags[0].Value != "id:https://ex" {
		t.Errorf("got {%s, %q}, want {WXXX, id:https://ex}", tags[0].ID, tags[0].Value)
	}
}

func TestParseUnknownMetadataFrame(t *testing.T) {
	frame := buildFrame("XABC", 4, []byte{0x03, 'c', 'u', 's', 't', 'o', 'm'})
	tag := buildTag(4, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ID != "XABC" || tags[0].Value != "custom" {
		t.Errorf("got {%s, %q}, want {XABC, custom}", tags[0].ID, tags[0].Value)
	}
}

func TestParseUTF8(t *testing.T) {
	frame := buildFrame("TIT2", 4, []byte{0x03, 0xC3, 0xBC, 0x62, 0x65, 0x72}) // "über" in UTF-8
	tag := buildTag(4, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "über" {
		t.Errorf("Value = %q, want 'über'", tags[0].Value)
	}
}

func TestParseUTF16BOM(t *testing.T) {
	// UTF-16LE BOM + "Hi"
	data := []byte{0x01, 0xFF, 0xFE, 'H', 0x00, 'i', 0x00}
	frame := buildFrame("TIT2", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "Hi" {
		t.Errorf("Value = %q, want 'Hi'", tags[0].Value)
	}
}

func TestParseMultipleFrames(t *testing.T) {
	f1 := buildFrame("TIT2", 3, []byte{0x00, 'A'})
	f2 := buildFrame("TIT3", 3, []byte{0x00, 'B'})
	tag := buildTag(3, f1, f2)
	tags, _ := Parse(tag)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}
}

func TestParseNonZeroOffset(t *testing.T) {
	prefix := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00}
	frame := buildFrame("TIT2", 3, []byte{0x00, 'X'})
	tag := buildTag(3, frame)
	data := append(prefix, tag...)
	tags, _ := Parse(data)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag at non-zero offset, got %d", len(tags))
	}
}

func TestParseTruncatedHeader(t *testing.T) {
	// Only 5 bytes — not enough for header
	data := []byte{'I', 'D', '3', 4, 0}
	tags, _ := Parse(data)
	if len(tags) != 0 {
		t.Errorf("expected 0 tags for truncated header, got %d", len(tags))
	}
}

func TestParseEmptyInput(t *testing.T) {
	tags, err := Parse(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tags) != 0 {
		t.Errorf("expected 0 tags for nil input, got %d", len(tags))
	}
}

func TestParseNoID3(t *testing.T) {
	data := []byte("This is just some audio data with no tags")
	tags, _ := Parse(data)
	if len(tags) != 0 {
		t.Errorf("expected 0 tags, got %d", len(tags))
	}
}

func TestParseMultipleTags(t *testing.T) {
	f1 := buildFrame("TIT2", 3, []byte{0x00, 'A'})
	tag1 := buildTag(3, f1)
	f2 := buildFrame("TIT2", 3, []byte{0x00, 'B'})
	tag2 := buildTag(3, f2)
	data := append(tag1, tag2...)
	tags, _ := Parse(data)
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags from 2 ID3 blocks, got %d", len(tags))
	}
}

func TestScannerParsesTagAcrossChunkBoundaries(t *testing.T) {
	frame := buildFrame("TIT2", 3, []byte{0x00, 'S', 't', 'r', 'e', 'a', 'm'})
	tag := buildTag(3, frame)
	data := append([]byte("prefix"), tag...)
	data = append(data, []byte("suffix")...)

	scanner := NewScanner(1024)
	var tags []Tag
	for _, chunkSize := range []int{3, 2, 5, 1, 4, 8, 64} {
		if len(data) == 0 {
			break
		}
		if chunkSize > len(data) {
			chunkSize = len(data)
		}
		got, err := scanner.Write(data[:chunkSize])
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		tags = append(tags, got...)
		data = data[chunkSize:]
	}
	got, err := scanner.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	tags = append(tags, got...)

	if len(tags) != 1 {
		t.Fatalf("tags = %d, want 1", len(tags))
	}
	if tags[0].ID != "TIT2" || tags[0].Value != "Stream" {
		t.Fatalf("tag = {%s %q}, want {TIT2 Stream}", tags[0].ID, tags[0].Value)
	}
}

func TestScannerRejectsOversizedTag(t *testing.T) {
	frame := buildFrame("TIT2", 3, bytes.Repeat([]byte{'x'}, 32))
	tag := buildTag(3, frame)

	scanner := NewScanner(8)
	_, err := scanner.Write(tag)
	if err == nil {
		t.Fatal("expected oversized tag error")
	}
	if !strings.Contains(err.Error(), "ID3 tag too large") {
		t.Fatalf("error = %q, want ID3 tag too large", err.Error())
	}
}

func TestParseSynchsafe(t *testing.T) {
	// Test synchsafe decoding directly
	got := decodeSynchsafe([]byte{0x00, 0x00, 0x02, 0x01}) // 0*2^21 + 0*2^14 + 2*2^7 + 1 = 257
	if got != 257 {
		t.Errorf("decodeSynchsafe = %d, want 257", got)
	}
}

func TestParseSynchsafeInvalid(t *testing.T) {
	got := decodeSynchsafe([]byte{0x80, 0x00, 0x00, 0x00}) // >= 0x80 is invalid
	if got != 0 {
		t.Errorf("decodeSynchsafe with invalid byte = %d, want 0", got)
	}
}

func TestParseTrailingNulls(t *testing.T) {
	frame := buildFrame("TIT2", 3, []byte{0x00, 'H', 'i', 0x00, 0x00})
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "Hi" {
		t.Errorf("Value = %q, want 'Hi'", tags[0].Value)
	}
}

func TestParseExtendedHeader(t *testing.T) {
	frame := buildFrame("TIT2", 4, []byte{0x03, 'O', 'K'})

	// Build tag with extended header flag
	var frameData []byte
	// Extended header: synchsafe size=6 (header is 6 bytes total: 4 size + 1 numflags + 1 flags)
	extHeader := []byte{0x00, 0x00, 0x00, 0x06, 0x01, 0x00}
	frameData = append(frameData, extHeader...)
	frameData = append(frameData, frame...)

	header := make([]byte, 10)
	header[0] = 'I'
	header[1] = 'D'
	header[2] = '3'
	header[3] = 4    // v2.4
	header[4] = 0    // revision
	header[5] = 0x40 // extended header flag

	size := len(frameData)
	header[6] = byte((size >> 21) & 0x7F)
	header[7] = byte((size >> 14) & 0x7F)
	header[8] = byte((size >> 7) & 0x7F)
	header[9] = byte(size & 0x7F)

	data := append(header, frameData...)
	tags, _ := Parse(data)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag with extended header, got %d", len(tags))
	}
	if tags[0].Value != "OK" {
		t.Errorf("Value = %q, want 'OK'", tags[0].Value)
	}
}

func TestGenericTextFrameTPE1(t *testing.T) {
	frame := buildFrame("TPE1", 3, []byte{0x00, 'A', 'r', 't', 'i', 's', 't'})
	tag := buildTag(3, frame)
	tags, err := Parse(tag)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].ID != "TPE1" || tags[0].Value != "Artist" {
		t.Errorf("got {%s, %q}, want {TPE1, Artist}", tags[0].ID, tags[0].Value)
	}
}

func TestGenericTextFrameTALB(t *testing.T) {
	frame := buildFrame("TALB", 3, []byte{0x00, 'A', 'l', 'b', 'u', 'm'})
	tag := buildTag(3, frame)
	tags, err := Parse(tag)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(tags) != 1 || tags[0].ID != "TALB" || tags[0].Value != "Album" {
		t.Errorf("got %v, want [{TALB Album}]", tags)
	}
}

func TestTXXXNotCaughtByGenericTFrame(t *testing.T) {
	// TXXX must still use parseTXXXFrame, which produces "desc:value" format.
	// The generic T-frame path would lose the description prefix.
	data := []byte{0x00, 'd', 'e', 's', 'c', 0x00, 'v', 'a', 'l'}
	frame := buildFrame("TXXX", 3, data)
	tag := buildTag(3, frame)
	tags, _ := Parse(tag)
	if len(tags) != 1 {
		t.Fatalf("expected 1 tag, got %d", len(tags))
	}
	if tags[0].Value != "desc:val" {
		t.Errorf("TXXX Value = %q, want 'desc:val'", tags[0].Value)
	}
}

func TestParseFromMPEGTSFallback(t *testing.T) {
	// Non-MPEGTS input: doesn't start with 0x47, len is not a multiple of 188.
	// ParseFromMPEGTS must fall back to Parse and return the same result.
	frame := buildFrame("TIT2", 3, []byte{0x00, 'X'})
	data := buildTag(3, frame)
	// data is ~20 bytes; not divisible by 188, not a sync byte at [0]
	groups, err := ParseFromMPEGTS(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].ID != "TIT2" || groups[0][0].Value != "X" {
		t.Errorf("fallback: got %v, want [[{TIT2 X}]]", groups)
	}
}

func TestParseFromMPEGTSFallbackWrongSyncByte(t *testing.T) {
	// Exactly 188 bytes (divisible by 188) but does not start with 0x47.
	// ParseFromMPEGTS must still fall back to Parse, not treat this as MPEGTS.
	// Real-world case: a 188-byte AAC segment with an ID3 tag prepended.
	frame := buildFrame("TIT2", 3, []byte{0x00, 'Y'})
	id3tag := buildTag(3, frame) // ~22 bytes, starts with 'I' (0x49)
	// Pad to exactly 188 bytes
	data := make([]byte, 188)
	copy(data, id3tag)
	// data[0] == 'I' (0x49), not 0x47; len(data) == 188 (divisible by 188)
	groups, err := ParseFromMPEGTS(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].ID != "TIT2" || groups[0][0].Value != "Y" {
		t.Errorf("fallback (wrong sync byte): got %v, want [[{TIT2 Y}]]", groups)
	}
}

func TestParseFromMPEGTSSinglePacket(t *testing.T) {
	// ID3 tag small enough to fit in one TS packet payload (184 bytes).
	f1 := buildFrame("TIT2", 3, []byte{0x00, 'T', 'i', 't', 'l', 'e'})
	f2 := buildFrame("TPE1", 3, []byte{0x00, 'A', 'r', 't', 'i', 's', 't'})
	id3data := buildTag(3, f1, f2)

	tsData := buildMPEGTSSegment(0x0104, id3data)
	if len(tsData) != 188 {
		t.Fatalf("test setup: expected 1 TS packet (188 bytes), got %d", len(tsData))
	}

	groups, err := ParseFromMPEGTS(tsData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("expected 1 group, got 0")
	}
	tagMap := make(map[string]string)
	for _, tag := range groups[0] {
		tagMap[tag.ID] = tag.Value
	}
	if tagMap["TIT2"] != "Title" {
		t.Errorf("TIT2 = %q, want 'Title'", tagMap["TIT2"])
	}
	if tagMap["TPE1"] != "Artist" {
		t.Errorf("TPE1 = %q, want 'Artist'", tagMap["TPE1"])
	}
}

func TestParseFromMPEGTSMultiPacket(t *testing.T) {
	// ID3 tag large enough to span two TS packet payloads.
	// Each TS payload = 184 bytes; PES header = 9 bytes.
	// So ID3 data > 175 bytes forces a second packet.
	// 200 chars exceeds the 175-byte single-packet limit (184-byte payload minus 9-byte PES header),
	// guaranteeing the title body is split across packet boundaries.
	longTitle := strings.Repeat("A", 200)
	f1 := buildFrame("TIT2", 3, append([]byte{0x00}, []byte(longTitle)...))
	f2 := buildFrame("TPE1", 3, []byte{0x00, 'B'})
	id3data := buildTag(3, f1, f2)

	tsData := buildMPEGTSSegment(0x0104, id3data)
	if len(tsData) < 188*2 {
		t.Fatalf("test setup: expected multi-packet data, got %d bytes", len(tsData))
	}

	groups, err := ParseFromMPEGTS(tsData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) == 0 {
		t.Fatal("expected 1 group, got 0")
	}
	tagMap := make(map[string]string)
	for _, tag := range groups[0] {
		tagMap[tag.ID] = tag.Value
	}
	if tagMap["TIT2"] != longTitle {
		t.Errorf("TIT2 = %q (len %d), want %q (len %d)",
			tagMap["TIT2"], len(tagMap["TIT2"]), longTitle, len(longTitle))
	}
	if tagMap["TPE1"] != "B" {
		t.Errorf("TPE1 = %q, want 'B'", tagMap["TPE1"])
	}
}

func TestParseFromMPEGTSMultiplePIDs(t *testing.T) {
	// Two PIDs in the same segment: one audio (no ID3), one timed_id3.
	// Only the timed_id3 PID should produce tags.
	audioData := buildMPEGTSSegment(0x0100, []byte("raw audio payload no metadata"))

	frame := buildFrame("TIT2", 3, []byte{0x00, 'X'})
	id3data := buildTag(3, frame)
	id3TS := buildMPEGTSSegment(0x0104, id3data)

	combined := append(audioData, id3TS...)

	groups, err := ParseFromMPEGTS(combined)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 || len(groups[0]) != 1 || groups[0][0].ID != "TIT2" || groups[0][0].Value != "X" {
		t.Errorf("got %v, want [[{TIT2 X}]]", groups)
	}
}

func TestParseFromMPEGTSNonID3PES(t *testing.T) {
	// TS segment whose PES payload does not contain ID3 magic.
	// Expect zero tags and no error.
	tsData := buildMPEGTSSegment(0x0100, []byte("raw audio payload no metadata"))
	groups, err := ParseFromMPEGTS(tsData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("expected 0 groups from non-ID3 PES, got %d: %v", len(groups), groups)
	}
}
