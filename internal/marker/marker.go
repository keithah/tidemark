package marker

import (
	"strconv"
	"time"
)

// StreamType identifies the detected stream protocol.
type StreamType int

const (
	StreamUnknown StreamType = iota
	StreamHLS
	StreamMPEGTS
	StreamICY
	StreamUDP
)

func (s StreamType) String() string {
	switch s {
	case StreamHLS:
		return "HLS"
	case StreamMPEGTS:
		return "MPEGTS"
	case StreamICY:
		return "ICY"
	case StreamUDP:
		return "UDP"
	default:
		return "Unknown"
	}
}

// MarkerType identifies the ad signaling protocol.
type MarkerType int

const (
	MarkerSCTE35 MarkerType = iota
	MarkerICY
	MarkerID3
)

func (m MarkerType) String() string {
	switch m {
	case MarkerSCTE35:
		return "SCTE35"
	case MarkerICY:
		return "ICY"
	case MarkerID3:
		return "ID3"
	default:
		return "Unknown"
	}
}

func (m MarkerType) MarshalJSON() ([]byte, error) {
	return strconv.AppendQuote(nil, m.String()), nil
}

// Classification identifies the ad transition type.
type Classification int

const (
	Unknown Classification = iota
	AdStart
	AdEnd
)

func (c Classification) String() string {
	switch c {
	case AdStart:
		return "AD_START"
	case AdEnd:
		return "AD_END"
	default:
		return "METADATA"
	}
}

func (c Classification) MarshalJSON() ([]byte, error) {
	return strconv.AppendQuote(nil, c.String()), nil
}

// DetectResult holds the stream detection outcome.
type DetectResult struct {
	Type    StreamType
	MetaInt int // ICY metaint value, if detected
}

// SCTE35Details is the typed internal representation used for classification.
// Fields remains on Marker for stable JSON/detail output.
type SCTE35Details struct {
	CommandName           string
	OutOfNetworkIndicator *bool
	BreakDuration         float64
	SpliceEventID         string
	SegmentationTypeID    uint8
}

// Marker is the canonical ad marker type emitted by all detectors.
type Marker struct {
	Type           MarkerType        `json:"Type"`
	Classification Classification    `json:"Classification"`
	Source         string            `json:"Source"`
	Tag            string            `json:"Tag,omitempty"`
	PTS            float64           `json:"PTS,omitempty"`
	Segment        int               `json:"Segment,omitempty"`
	RawB64         string            `json:"RawBase64,omitempty"`
	Tags           map[string]string `json:"Tags,omitempty"`
	Fields         map[string]string `json:"Fields,omitempty"`
	SCTE35         *SCTE35Details    `json:"-"`
	Timestamp      time.Time         `json:"Timestamp"`
}
