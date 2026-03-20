package classifier

import (
	"strings"
	"unicode"

	"github.com/keithah/tidemark/internal/marker"
)

// icyAdKeywords are words that indicate an ad in ICY StreamTitle.
var icyAdKeywords = []string{"ad", "spot", "promo", "commercial"}

// id3AdStartKeywords indicate ad start in ID3 frame content.
var id3AdStartKeywords = []string{"ad", "spot", "promo", "commercial"}

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

	// Split title into words on non-alphanumeric boundaries
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	hasAdKeyword := false
	for _, word := range words {
		for _, kw := range icyAdKeywords {
			if strings.EqualFold(word, kw) {
				hasAdKeyword = true
				break
			}
		}
		if hasAdKeyword {
			break
		}
	}

	if hasAdKeyword {
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
	if m.Fields == nil {
		return marker.Unknown
	}

	cmdName := m.Fields["CommandName"]

	switch {
	case cmdName == "Splice Insert":
		oon := m.Fields["OutOfNetworkIndicator"]
		if oon == "true" {
			return marker.AdStart
		}
		return marker.AdEnd

	case cmdName == "Time Signal":
		segType := m.Fields["SegmentationTypeID"]
		switch segType {
		case "0x22", "0x30", "0x34":
			return marker.AdStart
		case "0x23", "0x31", "0x35":
			return marker.AdEnd
		}
		return marker.Unknown

	case cmdName == "Splice Null":
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
		words := strings.FieldsFunc(lower, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		for _, word := range words {
			for _, kw := range id3AdStartKeywords {
				if word == kw {
					return marker.AdStart
				}
			}
		}
	}

	return marker.Unknown
}
