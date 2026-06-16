package id3

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Tag represents a parsed ID3v2 frame.
type Tag struct {
	ID    string
	Value string
}

var id3Marker = []byte("ID3")

// Scanner incrementally extracts complete ID3 tags from a byte stream.
type Scanner struct {
	maxTagBytes int
	buf         []byte
}

// NewScanner creates an incremental ID3 scanner with a maximum complete tag size.
func NewScanner(maxTagBytes int) *Scanner {
	if maxTagBytes <= 0 {
		maxTagBytes = 1 << 20
	}
	return &Scanner{maxTagBytes: maxTagBytes}
}

// Write appends stream bytes and returns any complete ID3 tags found.
func (s *Scanner) Write(data []byte) ([]Tag, error) {
	if len(data) == 0 {
		return nil, nil
	}
	s.buf = append(s.buf, data...)
	return s.scan(false)
}

// Flush returns complete tags still buffered and discards incomplete trailing bytes.
func (s *Scanner) Flush() ([]Tag, error) {
	return s.scan(true)
}

func (s *Scanner) scan(flush bool) ([]Tag, error) {
	tags := make([]Tag, 0, 4)
	needsCompact := false
	compactIfNeeded := func() {
		if needsCompact {
			s.compact()
		}
	}
	for {
		idx := bytes.Index(s.buf, id3Marker)
		if idx < 0 {
			if flush || len(s.buf) <= 2 {
				if flush {
					s.buf = nil
				}
				return tags, nil
			}
			s.keepTail(2)
			return tags, nil
		}
		if idx > 0 {
			s.advance(idx)
			needsCompact = true
		}
		if len(s.buf) < 10 {
			if flush {
				s.buf = nil
			} else {
				compactIfNeeded()
			}
			return tags, nil
		}
		version := int(s.buf[3])
		if version != 3 && version != 4 {
			s.advance(3)
			needsCompact = true
			continue
		}
		sizeBytes := s.buf[6:10]
		validSize := true
		for _, b := range sizeBytes {
			if b >= 0x80 {
				validSize = false
				break
			}
		}
		if !validSize {
			s.advance(3)
			needsCompact = true
			continue
		}
		tagSize := decodeSynchsafe(sizeBytes)
		if tagSize <= 0 {
			s.advance(10)
			needsCompact = true
			continue
		}
		total := 10 + tagSize
		if total > s.maxTagBytes {
			compactIfNeeded()
			return tags, fmt.Errorf("ID3 tag too large: exceeds %d bytes", s.maxTagBytes)
		}
		if len(s.buf) < total {
			if flush {
				s.buf = nil
			} else {
				compactIfNeeded()
			}
			return tags, nil
		}
		found, err := Parse(s.buf[:total])
		if err != nil {
			compactIfNeeded()
			return tags, err
		}
		tags = append(tags, found...)
		s.advance(total)
		needsCompact = true
	}
}

func (s *Scanner) advance(n int) {
	if n <= 0 {
		return
	}
	if n >= len(s.buf) {
		s.buf = s.buf[:0]
		return
	}
	s.buf = s.buf[n:]
}

func (s *Scanner) keepTail(n int) {
	if n <= 0 || len(s.buf) == 0 {
		s.buf = nil
		return
	}
	if n > len(s.buf) {
		n = len(s.buf)
	}
	tail := append([]byte(nil), s.buf[len(s.buf)-n:]...)
	s.buf = tail
}

func (s *Scanner) compact() {
	if len(s.buf) == 0 {
		s.buf = s.buf[:0]
		return
	}
	s.buf = append([]byte(nil), s.buf...)
}

// Parse scans raw bytes for ID3v2 tags and extracts frames.
// Returns all found tags. Supports v2.3 and v2.4.
func Parse(data []byte) ([]Tag, error) {
	if len(data) == 0 {
		return nil, nil
	}

	tags := make([]Tag, 0, 4)
	offset := 0

	for {
		idx := bytes.Index(data[offset:], id3Marker)
		if idx < 0 {
			break
		}
		offset += idx

		// Need at least 10 bytes for header
		if offset+10 > len(data) {
			break
		}

		header := data[offset : offset+10]
		version := int(header[3]) // major version
		if version != 3 && version != 4 {
			offset += 3 // skip past this "ID3" and try again
			continue
		}

		flags := header[5]

		// Synchsafe size (4 bytes, each < 0x80)
		sizeBytes := header[6:10]
		for _, b := range sizeBytes {
			if b >= 0x80 {
				offset += 3
				continue
			}
		}
		tagSize := decodeSynchsafe(sizeBytes)
		if tagSize <= 0 {
			offset += 10
			continue
		}

		frameStart := offset + 10

		// Skip extended header if present
		if flags&0x40 != 0 && frameStart+4 <= len(data) {
			var extSize int
			if version == 4 {
				extSize = decodeSynchsafe(data[frameStart : frameStart+4])
			} else {
				extSize = int(binary.BigEndian.Uint32(data[frameStart : frameStart+4]))
			}
			frameStart += extSize
		}

		tagEnd := offset + 10 + tagSize
		if tagEnd > len(data) {
			tagEnd = len(data)
		}

		// Parse frames
		pos := frameStart
		for pos+10 <= tagEnd {
			frameID := string(data[pos : pos+4])
			// Validate frame ID (uppercase A-Z, digits 0-9)
			if !isValidFrameID(frameID) {
				break // Padding or garbage
			}

			var frameSize int
			if version == 4 {
				frameSize = decodeSynchsafe(data[pos+4 : pos+8])
			} else {
				frameSize = int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
			}

			// Skip 2 bytes of frame flags
			frameDataStart := pos + 10
			frameDataEnd := frameDataStart + frameSize

			if frameDataEnd > tagEnd || frameSize <= 0 {
				break
			}

			frameData := data[frameDataStart:frameDataEnd]
			tag, ok := parseFrame(frameID, frameData)
			if ok {
				tags = append(tags, tag)
			}

			pos = frameDataEnd
		}

		offset = tagEnd
	}

	return tags, nil
}

func parseFrame(id string, data []byte) (Tag, bool) {
	switch {
	case strings.HasPrefix(id, "T") && id != "TXXX":
		return parseTextFrame(id, data)
	case id == "TXXX":
		return parseTXXXFrame(data)
	case id == "PRIV":
		return parsePRIVFrame(data)
	case id == "GEOB":
		return parseGEOBFrame(data)
	case id == "LINK":
		return parseLINKFrame(data)
	case id == "WXXX":
		return parseWXXXFrame(data)
	case strings.HasPrefix(id, "W"):
		return parseURLFrame(id, data)
	case id == "COMM":
		return parseCOMMFrame(data)
	default:
		return parseGenericFrame(id, data)
	}
}

func parseTextFrame(id string, data []byte) (Tag, bool) {
	if len(data) < 2 {
		return Tag{}, false
	}
	encoding := data[0]
	text := decodeText(data[1:], encoding)
	return Tag{ID: id, Value: text}, true
}

func parseTXXXFrame(data []byte) (Tag, bool) {
	if len(data) < 2 {
		return Tag{}, false
	}
	encoding := data[0]
	rest := data[1:]

	// Split on null terminator (encoding-aware)
	desc, value := splitOnNull(rest, encoding)
	descText := decodeText(desc, encoding)
	valueText := decodeText(value, encoding)

	if descText != "" {
		return Tag{ID: "TXXX", Value: descText + ":" + valueText}, true
	}
	return Tag{ID: "TXXX", Value: valueText}, true
}

func parsePRIVFrame(data []byte) (Tag, bool) {
	// owner\x00binary_data
	nullIdx := bytes.IndexByte(data, 0)
	if nullIdx < 0 {
		return Tag{ID: "PRIV", Value: hex.EncodeToString(data)}, true
	}
	owner := string(data[:nullIdx])
	binary := data[nullIdx+1:]
	return Tag{ID: "PRIV", Value: owner + ":" + hex.EncodeToString(binary)}, true
}

func parseGEOBFrame(data []byte) (Tag, bool) {
	if len(data) < 4 {
		return Tag{}, false
	}
	encoding := data[0]
	rest := data[1:]

	// mime type (ISO-8859-1, null terminated)
	nullIdx := bytes.IndexByte(rest, 0)
	if nullIdx < 0 {
		return Tag{}, false
	}
	mime := string(rest[:nullIdx])
	rest = rest[nullIdx+1:]

	// filename (encoding-aware)
	fn, remaining := splitOnNull(rest, encoding)
	filename := decodeText(fn, encoding)

	// description (encoding-aware)
	desc, objData := splitOnNull(remaining, encoding)
	description := decodeText(desc, encoding)

	return Tag{
		ID:    "GEOB",
		Value: fmt.Sprintf("%s:%s:%s:%s", mime, filename, description, hex.EncodeToString(objData)),
	}, true
}

func parseLINKFrame(data []byte) (Tag, bool) {
	if len(data) == 0 {
		return Tag{}, false
	}

	// ID3 LINK frames start with a 4-byte linked frame ID. In the Stingray
	// timed metadata this is followed by a null separator and the useful ID.
	if len(data) >= 4 && isValidFrameID(string(data[:4])) {
		data = data[4:]
	}
	data = bytes.Trim(data, "\x00")
	if len(data) == 0 {
		return Tag{}, false
	}
	return Tag{ID: "LINK", Value: string(data)}, true
}

func parseWXXXFrame(data []byte) (Tag, bool) {
	if len(data) < 2 {
		return Tag{}, false
	}
	encoding := data[0]
	desc, value := splitOnNull(data[1:], encoding)
	descText := decodeText(desc, encoding)
	valueText := strings.Trim(string(bytes.Trim(value, "\x00")), "\x00")
	if valueText == "" {
		return Tag{}, false
	}
	if descText != "" {
		return Tag{ID: "WXXX", Value: descText + ":" + valueText}, true
	}
	return Tag{ID: "WXXX", Value: valueText}, true
}

func parseURLFrame(id string, data []byte) (Tag, bool) {
	value := strings.TrimSpace(string(bytes.Trim(data, "\x00")))
	if value == "" {
		return Tag{}, false
	}
	return Tag{ID: id, Value: value}, true
}

func parseCOMMFrame(data []byte) (Tag, bool) {
	if len(data) < 5 {
		return Tag{}, false
	}
	encoding := data[0]
	lang := string(data[1:4])
	desc, text := splitOnNull(data[4:], encoding)
	descText := decodeText(desc, encoding)
	valueText := decodeText(text, encoding)
	if valueText == "" {
		return Tag{}, false
	}
	switch {
	case descText != "" && lang != "":
		return Tag{ID: "COMM", Value: lang + ":" + descText + ":" + valueText}, true
	case lang != "":
		return Tag{ID: "COMM", Value: lang + ":" + valueText}, true
	default:
		return Tag{ID: "COMM", Value: valueText}, true
	}
}

func parseGenericFrame(id string, data []byte) (Tag, bool) {
	if len(data) == 0 {
		return Tag{}, false
	}
	if data[0] <= 0x03 {
		if text := strings.TrimSpace(decodeText(data[1:], data[0])); text != "" {
			return Tag{ID: id, Value: text}, true
		}
	}

	value := strings.TrimSpace(strings.Join(strings.Fields(string(bytes.Trim(data, "\x00"))), " "))
	if value == "" {
		value = hex.EncodeToString(data)
	}
	return Tag{ID: id, Value: value}, true
}

func decodeText(data []byte, encoding byte) string {
	switch encoding {
	case 0x00: // ISO-8859-1
		data = bytes.TrimRight(data, "\x00")
		return string(data)
	case 0x01: // UTF-16 with BOM
		return decodeUTF16(data)
	case 0x02: // UTF-16BE without BOM
		return decodeUTF16BE(data)
	case 0x03: // UTF-8
		data = bytes.TrimRight(data, "\x00")
		return string(data)
	default:
		data = bytes.TrimRight(data, "\x00")
		return string(data)
	}
}

func decodeUTF16(data []byte) string {
	// Trim double-null for UTF-16
	for len(data) >= 2 && data[len(data)-1] == 0 && data[len(data)-2] == 0 {
		data = data[:len(data)-2]
	}
	if len(data) < 2 {
		return ""
	}
	// Check BOM
	var bigEndian bool
	if data[0] == 0xFE && data[1] == 0xFF {
		bigEndian = true
		data = data[2:]
	} else if data[0] == 0xFF && data[1] == 0xFE {
		bigEndian = false
		data = data[2:]
	}

	u16s := make([]uint16, len(data)/2)
	for i := 0; i < len(u16s); i++ {
		if bigEndian {
			u16s[i] = uint16(data[i*2])<<8 | uint16(data[i*2+1])
		} else {
			u16s[i] = uint16(data[i*2+1])<<8 | uint16(data[i*2])
		}
	}
	return string(utf16.Decode(u16s))
}

func decodeUTF16BE(data []byte) string {
	for len(data) >= 2 && data[len(data)-1] == 0 && data[len(data)-2] == 0 {
		data = data[:len(data)-2]
	}
	if len(data) < 2 {
		return ""
	}
	u16s := make([]uint16, len(data)/2)
	for i := 0; i < len(u16s); i++ {
		u16s[i] = uint16(data[i*2])<<8 | uint16(data[i*2+1])
	}
	return string(utf16.Decode(u16s))
}

func splitOnNull(data []byte, encoding byte) ([]byte, []byte) {
	if encoding == 0x01 || encoding == 0x02 {
		// UTF-16: double-null separator
		for i := 0; i+1 < len(data); i += 2 {
			if data[i] == 0 && data[i+1] == 0 {
				return data[:i], data[i+2:]
			}
		}
		return data, nil
	}
	// Single-byte encodings: single null
	idx := bytes.IndexByte(data, 0)
	if idx < 0 {
		return data, nil
	}
	return data[:idx], data[idx+1:]
}

func decodeSynchsafe(b []byte) int {
	if len(b) != 4 {
		return 0
	}
	for _, v := range b {
		if v >= 0x80 {
			return 0
		}
	}
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3])
}

func isValidFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for _, c := range id {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// ParseFromMPEGTS extracts ID3 tags from data that may be raw MPEGTS.
//
// If data looks like MPEGTS (starts with sync byte 0x47, length is a multiple
// of 188), it walks the TS packets, reassembles PES payloads per PID, strips
// PES headers, and calls Parse on each ID3 blob found. Otherwise it falls back
// to Parse directly — so raw AAC segments and non-TS input work unchanged.
//
// Each inner slice represents one distinct timed ID3 event (one ID3 blob/PES).
func ParseFromMPEGTS(data []byte) ([][]Tag, error) {
	if len(data) < 188 || data[0] != 0x47 || len(data)%188 != 0 {
		tags, err := Parse(data)
		if len(tags) == 0 {
			return nil, err
		}
		return [][]Tag{tags}, err
	}

	bufs := make(map[uint16][]byte)
	var id3Blobs [][]byte

	// collect checks the first 32 bytes of buf for "ID3" magic and, if found,
	// appends buf[idx:] to id3Blobs. 32 bytes is enough to cover any PES header
	// (max ~19 bytes) plus the 3-byte magic.
	collect := func(buf []byte) {
		if len(buf) == 0 {
			return
		}
		limit := len(buf)
		if limit > 32 {
			limit = 32
		}
		idx := bytes.Index(buf[:limit], []byte("ID3"))
		if idx >= 0 {
			id3Blobs = append(id3Blobs, buf[idx:])
		}
	}

	for i := 0; i+188 <= len(data); i += 188 {
		pkt := data[i : i+188]
		if pkt[0] != 0x47 {
			continue // lost sync, skip
		}

		pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
		pusi := pkt[1]&0x40 != 0
		afc := (pkt[3] >> 4) & 0x03

		if afc&0x01 == 0 {
			continue // no payload in this packet
		}

		// Determine where the payload starts within the 188-byte packet.
		// Bytes 0-3 are the TS header. If an adaptation field is present
		// (afc & 0x02), byte 4 is its length and we skip past it.
		payloadStart := 4
		if afc&0x02 != 0 {
			payloadStart = 5 + int(pkt[4])
		}
		if payloadStart >= 188 {
			continue
		}
		payload := pkt[payloadStart:]

		if pusi {
			// A new PES is starting on this PID. Flush whatever was
			// accumulated for this PID (it's a completed PES).
			collect(bufs[pid])
			newBuf := make([]byte, len(payload))
			copy(newBuf, payload)
			bufs[pid] = newBuf
		} else if _, ok := bufs[pid]; ok {
			// Continuation packet — only accumulate if we already have
			// a buffer for this PID (i.e., we saw its PUSI packet).
			bufs[pid] = append(bufs[pid], payload...)
		}
	}

	// Flush any PES still in progress at end of segment.
	for _, buf := range bufs {
		collect(buf)
	}

	var allGroups [][]Tag
	for _, blob := range id3Blobs {
		tags, err := Parse(blob)
		if err != nil {
			_ = err // non-fatal: collect whatever frames were parsed
		}
		if len(tags) > 0 {
			allGroups = append(allGroups, tags)
		}
	}
	return allGroups, nil
}
