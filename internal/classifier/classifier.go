package classifier

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/keithah/tidemark/internal/marker"
)

// icyAdKeywords are words that indicate an ad in ICY StreamTitle.
var icyAdKeywords = map[string]struct{}{
	"ad":         {},
	"spot":       {},
	"promo":      {},
	"commercial": {},
}

// id3AdStartKeywords indicate ad start in ID3 frame content.
var id3AdStartKeywords = map[string]struct{}{
	"ad":         {},
	"spot":       {},
	"promo":      {},
	"commercial": {},
}

// id3AdEndKeywords indicate ad end in ID3 frame content.
var id3AdEndKeywords = []string{"ad_end", "content_start"}

// Classifier classifies markers as AD_START, AD_END, or UNKNOWN.
// It is stateful for ICY (tracks inAd state for AD_END detection)
// and stateless for SCTE-35 and ID3.
type Classifier struct {
	inAd bool // ICY state: true when we've seen an ad keyword
}

// New creates a new Classifier.
func New() *Classifier {
	return &Classifier{}
}

// Classify sets the Classification field on the given marker.
func (c *Classifier) Classify(m *marker.Marker) marker.Classification {
	switch m.Type {
	case marker.MarkerICY:
		return c.classifyICY(m)
	case marker.MarkerSCTE35:
		return classifySCTE35(m)
	case marker.MarkerID3:
		return classifyID3(m)
	default:
		return marker.Unknown
	}
}

func (c *Classifier) classifyICY(m *marker.Marker) marker.Classification {
	title := m.Fields["StreamTitle"]
	if title == "" {
		return marker.Unknown
	}

	if containsKeywordToken(title, icyAdKeywords) {
		c.inAd = true
		return marker.AdStart
	}

	if c.inAd {
		c.inAd = false
		return marker.AdEnd
	}

	return marker.Unknown
}

func classifySCTE35(m *marker.Marker) marker.Classification {
	if m.SCTE35 != nil {
		return classifyTypedSCTE35(m.SCTE35)
	}

	if m.Fields == nil {
		return marker.Unknown
	}

	return classifySCTE35Rule(scte35RuleFromFields(m.Fields))
}

func classifyTypedSCTE35(details *marker.SCTE35Details) marker.Classification {
	rule := scte35Rule{commandName: details.CommandName}
	if details.OutOfNetworkIndicator != nil {
		rule.outOfNetworkKnown = true
		rule.outOfNetwork = *details.OutOfNetworkIndicator
	}
	if details.SegmentationTypeID != 0 {
		rule.segmentationTypeKnown = true
		rule.segmentationTypeID = int(details.SegmentationTypeID)
	}
	return classifySCTE35Rule(rule)
}

type scte35Rule struct {
	commandName           string
	outOfNetworkKnown     bool
	outOfNetwork          bool
	segmentationTypeKnown bool
	segmentationTypeID    int
}

func scte35RuleFromFields(fields map[string]string) scte35Rule {
	rule := scte35Rule{commandName: fields["CommandName"]}
	if oon, ok := fields["OutOfNetworkIndicator"]; ok {
		rule.outOfNetworkKnown = true
		rule.outOfNetwork = oon == "true"
	}
	if segType, ok := parseSegmentationType(fields["SegmentationTypeID"]); ok {
		rule.segmentationTypeKnown = true
		rule.segmentationTypeID = segType
	}
	return rule
}

func parseSegmentationType(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	base := 10
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		value = value[2:]
		base = 16
	}
	n, err := strconv.ParseInt(value, base, 0)
	if err != nil {
		return 0, false
	}
	return int(n), true
}

func classifySCTE35Rule(rule scte35Rule) marker.Classification {
	switch rule.commandName {
	case "Splice Insert":
		if rule.outOfNetworkKnown && rule.outOfNetwork {
			return marker.AdStart
		}
		return marker.AdEnd

	case "Time Signal":
		if !rule.segmentationTypeKnown {
			return marker.Unknown
		}
		switch rule.segmentationTypeID {
		case 0x22, 0x30, 0x34:
			return marker.AdStart
		case 0x23, 0x31, 0x35:
			return marker.AdEnd
		}
		return marker.Unknown

	case "Splice Null":
		return marker.Unknown

	default:
		return marker.Unknown
	}
}

func classifyID3(m *marker.Marker) marker.Classification {
	// Check all tag values for ad keywords
	for _, value := range m.Tags {
		lower := strings.ToLower(value)

		// Check AD_END keywords first (ad_end contains "ad", so check before AD_START)
		for _, kw := range id3AdEndKeywords {
			if strings.Contains(lower, kw) {
				return marker.AdEnd
			}
		}

		// Check AD_START keywords using word boundary matching
		if containsKeywordTokenNormalized(lower, id3AdStartKeywords) {
			return marker.AdStart
		}
	}

	return marker.Unknown
}

func containsKeywordToken(text string, keywords map[string]struct{}) bool {
	start := -1
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if _, ok := keywords[strings.ToLower(text[start:i])]; ok {
				return true
			}
			start = -1
		}
	}
	if start < 0 {
		return false
	}
	_, ok := keywords[strings.ToLower(text[start:])]
	return ok
}

func containsKeywordTokenNormalized(text string, keywords map[string]struct{}) bool {
	start := -1
	for i, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if _, ok := keywords[text[start:i]]; ok {
				return true
			}
			start = -1
		}
	}
	if start < 0 {
		return false
	}
	_, ok := keywords[text[start:]]
	return ok
}
