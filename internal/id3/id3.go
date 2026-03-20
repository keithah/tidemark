package id3

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"unicode/utf16"
)

// Tag represents a parsed ID3v2 frame.
type Tag struct {
	ID    string
	Value string
}

// Parse scans raw bytes for ID3v2 tags and extracts frames.
// Returns all found tags. Supports v2.3 and v2.4.
func Parse(data []byte) ([]Tag, error) {
	if len(data) == 0 {
		return nil, nil
	}

	var tags []Tag
	offset := 0

	for {
		idx := bytes.Index(data[offset:], []byte("ID3"))
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
			tag := parseFrame(frameID, frameData)
			if tag != nil {
				tags = append(tags, *tag)
			}

			pos = frameDataEnd
		}

		offset = tagEnd
	}

	return tags, nil
}

func parseFrame(id string, data []byte) *Tag {
	switch {
	case id == "TIT2" || id == "TIT3":
		return parseTextFrame(id, data)
	case id == "TXXX":
		return parseTXXXFrame(data)
	case id == "PRIV":
		return parsePRIVFrame(data)
	case id == "GEOB":
		return parseGEOBFrame(data)
	default:
		return nil
	}
}

func parseTextFrame(id string, data []byte) *Tag {
	if len(data) < 2 {
		return nil
	}
	encoding := data[0]
	text := decodeText(data[1:], encoding)
	return &Tag{ID: id, Value: text}
}

func parseTXXXFrame(data []byte) *Tag {
	if len(data) < 2 {
		return nil
	}
	encoding := data[0]
	rest := data[1:]

	// Split on null terminator (encoding-aware)
	desc, value := splitOnNull(rest, encoding)
	descText := decodeText(desc, encoding)
	valueText := decodeText(value, encoding)

	if descText != "" {
		return &Tag{ID: "TXXX", Value: descText + ":" + valueText}
	}
	return &Tag{ID: "TXXX", Value: valueText}
}

func parsePRIVFrame(data []byte) *Tag {
	// owner\x00binary_data
	nullIdx := bytes.IndexByte(data, 0)
	if nullIdx < 0 {
		return &Tag{ID: "PRIV", Value: hex.EncodeToString(data)}
	}
	owner := string(data[:nullIdx])
	binary := data[nullIdx+1:]
	return &Tag{ID: "PRIV", Value: owner + ":" + hex.EncodeToString(binary)}
}

func parseGEOBFrame(data []byte) *Tag {
	if len(data) < 4 {
		return nil
	}
	encoding := data[0]
	rest := data[1:]

	// mime type (ISO-8859-1, null terminated)
	nullIdx := bytes.IndexByte(rest, 0)
	if nullIdx < 0 {
		return nil
	}
	mime := string(rest[:nullIdx])
	rest = rest[nullIdx+1:]

	// filename (encoding-aware)
	fn, remaining := splitOnNull(rest, encoding)
	filename := decodeText(fn, encoding)

	// description (encoding-aware)
	desc, objData := splitOnNull(remaining, encoding)
	description := decodeText(desc, encoding)

	return &Tag{
		ID:    "GEOB",
		Value: fmt.Sprintf("%s:%s:%s:%s", mime, filename, description, hex.EncodeToString(objData)),
	}
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
