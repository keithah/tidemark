package hls

import (
	"strings"

	"github.com/keithah/tidemark/internal/marker"
)

// TagResult holds a parsed SCTE-35 tag result.
type TagResult struct {
	Payload    string // base64 or hex SCTE-35 payload
	Tag        string // original HLS tag name
	IsDirect   bool   // true for CUE-OUT/CUE-IN (no cuei decode needed)
	DirectType marker.Classification
	Attributes map[string]string // for DATERANGE
}

// ParseLine parses a single manifest line for SCTE-35 tags.
// Returns nil if the line is not a SCTE-35 tag.
func ParseLine(line string) *TagResult {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "#EXT-X-SCTE35:"):
		return parseSCTE35Tag(line)
	case strings.HasPrefix(line, "#EXT-OATCLS-SCTE35:"):
		return parseOATCLSTag(line)
	case strings.HasPrefix(line, "#EXT-X-DATERANGE:"):
		return parseDateRangeTag(line)
	case strings.HasPrefix(line, "#EXT-X-CUE-OUT"):
		return parseCueOutTag(line)
	case strings.HasPrefix(line, "#EXT-X-CUE-IN"):
		return parseCueInTag(line)
	default:
		return nil
	}
}

func parseSCTE35Tag(line string) *TagResult {
	// #EXT-X-SCTE35:CUE=<base64>
	attrs := parseAttributes(line[len("#EXT-X-SCTE35:"):])
	payload := attrs["CUE"]
	if payload == "" {
		// Try raw payload (no CUE= prefix)
		payload = strings.TrimPrefix(line, "#EXT-X-SCTE35:")
		payload = strings.TrimSpace(payload)
	}
	if payload == "" {
		return nil
	}
	return &TagResult{Payload: payload, Tag: "#EXT-X-SCTE35"}
}

func parseOATCLSTag(line string) *TagResult {
	payload := strings.TrimPrefix(line, "#EXT-OATCLS-SCTE35:")
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	return &TagResult{Payload: payload, Tag: "#EXT-OATCLS-SCTE35"}
}

func parseDateRangeTag(line string) *TagResult {
	attrs := parseAttributes(line[len("#EXT-X-DATERANGE:"):])

	// Check for SCTE35-OUT or SCTE35-IN
	if payload, ok := attrs["SCTE35-OUT"]; ok && payload != "" {
		return &TagResult{Payload: payload, Tag: "#EXT-X-DATERANGE", Attributes: attrs}
	}
	if payload, ok := attrs["SCTE35-IN"]; ok && payload != "" {
		return &TagResult{Payload: payload, Tag: "#EXT-X-DATERANGE", Attributes: attrs}
	}
	return nil
}

func parseCueOutTag(line string) *TagResult {
	// #EXT-X-CUE-OUT or #EXT-X-CUE-OUT:DURATION=30
	return &TagResult{
		Tag:        "#EXT-X-CUE-OUT",
		IsDirect:   true,
		DirectType: marker.AdStart,
	}
}

func parseCueInTag(line string) *TagResult {
	return &TagResult{
		Tag:        "#EXT-X-CUE-IN",
		IsDirect:   true,
		DirectType: marker.AdEnd,
	}
}

// parseAttributes parses HLS key=value attributes, handling quoted values with commas.
func parseAttributes(s string) map[string]string {
	attrs := make(map[string]string)
	s = strings.TrimSpace(s)

	var key, value strings.Builder
	inQuote := false
	parsingKey := true

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == '=' && !inQuote && parsingKey:
			parsingKey = false
		case c == ',' && !inQuote:
			attrs[key.String()] = value.String()
			key.Reset()
			value.Reset()
			parsingKey = true
		default:
			if parsingKey {
				key.WriteByte(c)
			} else {
				value.WriteByte(c)
			}
		}
	}
	// Last pair
	if key.Len() > 0 {
		attrs[key.String()] = value.String()
	}
	return attrs
}
