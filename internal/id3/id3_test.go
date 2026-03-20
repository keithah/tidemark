package id3

import (
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
