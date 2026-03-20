package marker

import (
	"encoding/json"
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
	return json.Marshal(m.String())
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
		return "UNKNOWN"
	}
}

func (c Classification) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.String())
}

// DetectResult holds the stream detection outcome.
type DetectResult struct {
	Type    StreamType
	MetaInt int // ICY metaint value, if detected
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
	Command        interface{}       `json:"Command,omitempty"`
	Descriptors    interface{}       `json:"Descriptors,omitempty"`
	Tags           map[string]string `json:"Tags,omitempty"`
	Fields         map[string]string `json:"Fields,omitempty"`
	Timestamp      time.Time         `json:"Timestamp"`
}
