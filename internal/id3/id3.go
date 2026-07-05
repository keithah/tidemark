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

const mpegtsID3ProbeBytes = 32

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

		// Synchsafe size (4 bytes, each < 0x80). A non-synchsafe byte means this
		// is not a real ID3 header; skip past the marker and resync the scan.
		sizeBytes := header[6:10]
		validSize := true
		for _, b := range sizeBytes {
			if b >= 0x80 {
				validSize = false
				break
			}
		}
		if !validSize {
			offset += 3
			continue
		}
		tagSize := decodeSynchsafe(sizeBytes)
		if tagSize <= 0 {
			offset += 10
			continue
		}

		tagEnd := offset + 10 + tagSize
		if tagEnd > len(data) {
			tagEnd = len(data)
		}

		frameStart := offset + 10

		// Skip extended header if present. In ID3v2.4 the size field is synchsafe
		// and covers the whole extended header; in ID3v2.3 it is a plain uint32
		// that EXCLUDES its own 4 size bytes, so those must be added separately.
		if flags&0x40 != 0 && frameStart+4 <= len(data) {
			if version == 4 {
				frameStart += decodeSynchsafe(data[frameStart : frameStart+4])
			} else {
				frameStart += 4 + int(binary.BigEndian.Uint32(data[frameStart:frameStart+4]))
			}
		}

		// A malformed extended-header size can push frameStart out of range (or
		// wrap negative on 32-bit); bail on this tag rather than risk a panic.
		if frameStart < offset+10 || frameStart > tagEnd {
			offset = tagEnd
			continue
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

			// Skip 2 bytes of frame flags.
			frameDataStart := pos + 10

			// Compare against remaining space instead of frameDataStart+frameSize,
			// which could overflow to a negative int on 32-bit platforms for a
			// hostile frameSize and slip past the bounds check into a slice panic.
			if frameSize <= 0 || frameSize > tagEnd-frameDataStart {
				break
			}
			frameDataEnd := frameDataStart + frameSize

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

// decodeUTF16 decodes UTF-16 text, honoring a leading BOM and defaulting to
// little-endian when none is present (ID3 encoding 0x01).
func decodeUTF16(data []byte) string {
	data = trimUTF16Nulls(data)
	if len(data) < 2 {
		return ""
	}
	bigEndian := false
	if data[0] == 0xFE && data[1] == 0xFF {
		bigEndian = true
		data = data[2:]
	} else if data[0] == 0xFF && data[1] == 0xFE {
		data = data[2:]
	}
	return decodeUTF16Units(data, bigEndian)
}

// decodeUTF16BE decodes big-endian UTF-16 text with no BOM (ID3 encoding 0x02).
func decodeUTF16BE(data []byte) string {
	data = trimUTF16Nulls(data)
	if len(data) < 2 {
		return ""
	}
	return decodeUTF16Units(data, true)
}

func trimUTF16Nulls(data []byte) []byte {
	for len(data) >= 2 && data[len(data)-1] == 0 && data[len(data)-2] == 0 {
		data = data[:len(data)-2]
	}
	return data
}

func decodeUTF16Units(data []byte, bigEndian bool) string {
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

	var extractor MPEGTSExtractor
	for i := 0; i+188 <= len(data); i += 188 {
		extractor.PushPacket(data[i : i+188])
	}
	return extractor.Groups(), nil
}

// MPEGTSExtractor incrementally extracts timed-ID3 PES payloads from
// sync-aligned 188-byte MPEG-TS packets.
type MPEGTSExtractor struct {
	bufs   map[uint16][]byte
	groups [][]Tag
}

// PushPacket adds one sync-aligned MPEG-TS packet to the extractor.
func (e *MPEGTSExtractor) PushPacket(pkt []byte) {
	if len(pkt) != 188 || pkt[0] != 0x47 {
		return
	}
	pid := uint16(pkt[1]&0x1F)<<8 | uint16(pkt[2])
	pusi := pkt[1]&0x40 != 0
	afc := (pkt[3] >> 4) & 0x03
	if afc&0x01 == 0 {
		return
	}

	payloadStart := 4
	if afc&0x02 != 0 {
		payloadStart = 5 + int(pkt[4])
	}
	if payloadStart >= 188 {
		return
	}
	payload := pkt[payloadStart:]

	if pusi {
		e.collect(pid)
		if e.bufs == nil {
			e.bufs = make(map[uint16][]byte)
		}
		e.bufs[pid] = append(e.bufs[pid][:0], payload...)
		e.keepPotentialID3PES(pid)
		return
	}
	if e.bufs != nil {
		if _, ok := e.bufs[pid]; ok {
			e.bufs[pid] = append(e.bufs[pid], payload...)
			e.keepPotentialID3PES(pid)
		}
	}
}

// Groups flushes any in-progress PES payloads and returns extracted ID3 tag
// groups. Calling Groups more than once is safe; later calls return the same
// groups without duplicating flushed payloads.
func (e *MPEGTSExtractor) Groups() [][]Tag {
	for pid := range e.bufs {
		e.collect(pid)
	}
	e.bufs = nil
	return e.groups
}

func (e *MPEGTSExtractor) collect(pid uint16) {
	buf := e.bufs[pid]
	delete(e.bufs, pid)
	if len(buf) == 0 {
		return
	}
	limit := len(buf)
	if limit > 32 {
		limit = 32
	}
	idx := bytes.Index(buf[:limit], []byte("ID3"))
	if idx < 0 {
		return
	}
	tags, err := Parse(buf[idx:])
	if err == nil && len(tags) > 0 {
		e.groups = append(e.groups, tags)
	}
}

func (e *MPEGTSExtractor) keepPotentialID3PES(pid uint16) {
	buf := e.bufs[pid]
	if len(buf) == 0 {
		delete(e.bufs, pid)
		return
	}
	limit := len(buf)
	if limit > mpegtsID3ProbeBytes {
		limit = mpegtsID3ProbeBytes
	}
	idx := bytes.Index(buf[:limit], id3Marker)
	if idx < 0 {
		if len(buf) >= mpegtsID3ProbeBytes {
			delete(e.bufs, pid)
		}
		return
	}
	if idx > 0 {
		buf = append(buf[:0], buf[idx:]...)
		e.bufs[pid] = buf
	}
	if tagSize, ok := id3TotalSize(buf); ok && len(buf) > tagSize {
		e.bufs[pid] = buf[:tagSize]
	}
}

func id3TotalSize(buf []byte) (int, bool) {
	if len(buf) < 10 || !bytes.HasPrefix(buf, id3Marker) {
		return 0, false
	}
	version := buf[3]
	if version != 3 && version != 4 {
		return 0, false
	}
	size := decodeSynchsafe(buf[6:10])
	if size <= 0 {
		return 0, false
	}
	return 10 + size, true
}
