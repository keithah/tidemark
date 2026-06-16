package scte35

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/futzu/cuei"

	"github.com/keithah/tidemark/internal/marker"
)

// Decode decodes a SCTE-35 payload (base64 or hex) and returns a Marker.
// source is e.g. "hls_manifest", "mpegts". tag is the HLS tag name (or "").
func Decode(payload, source, tag string) (*marker.Marker, error) {
	// Recover from cuei panics
	var m *marker.Marker
	var decodeErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				decodeErr = fmt.Errorf("cuei panic: %v", r)
			}
		}()

		// If hex-encoded, convert to base64 first (cuei hex path is broken)
		if strings.HasPrefix(payload, "0x") || strings.HasPrefix(payload, "0X") {
			hexStr := payload[2:]
			raw, err := hex.DecodeString(hexStr)
			if err != nil {
				decodeErr = fmt.Errorf("hex decode: %w", err)
				return
			}
			payload = base64.StdEncoding.EncodeToString(raw)
		}

		cue := cuei.NewCue()
		ok := cue.Decode(payload)
		if !ok {
			decodeErr = fmt.Errorf("cuei decode returned false")
			return
		}

		m = MarkerFromCue(cue, source, tag)
	}()

	if decodeErr != nil {
		return nil, decodeErr
	}
	return m, nil
}

// MarkerFromCue converts a decoded cuei cue into the canonical marker shape.
func MarkerFromCue(cue *cuei.Cue, source, tag string) *marker.Marker {
	if cue == nil {
		return nil
	}

	details := &marker.SCTE35Details{}

	if cue.Command != nil {
		details.CommandName = cue.Command.Name

		if cue.Command.Name == "Splice Insert" {
			oon := cue.Command.OutOfNetworkIndicator
			details.OutOfNetworkIndicator = &oon
			if cue.Command.BreakDuration > 0 {
				details.BreakDuration = cue.Command.BreakDuration
			}
			details.SpliceEventID = fmt.Sprintf("0x%x", cue.Command.SpliceEventID)
		}
	}

	var pts float64
	if cue.Command != nil && cue.Command.PTS > 0 {
		pts = cue.Command.PTS
	}

	// Extract segmentation type from descriptors
	if len(cue.Descriptors) > 0 {
		for _, d := range cue.Descriptors {
			if d.SegmentationTypeID > 0 {
				details.SegmentationTypeID = d.SegmentationTypeID
			}
		}
	}

	m := &marker.Marker{
		Type:   marker.MarkerSCTE35,
		Source: source,
		Tag:    tag,
		PTS:    pts,
		RawB64: "",
		Fields: detailsFields(*details),
		SCTE35: details,
	}
	return m
}

func detailsFields(details marker.SCTE35Details) map[string]string {
	fields := make(map[string]string, 5)
	if details.CommandName != "" {
		fields["CommandName"] = details.CommandName
	}
	if details.OutOfNetworkIndicator != nil {
		fields["OutOfNetworkIndicator"] = fmt.Sprintf("%v", *details.OutOfNetworkIndicator)
	}
	if details.BreakDuration > 0 {
		fields["BreakDuration"] = fmt.Sprintf("%.3f", details.BreakDuration)
	}
	if details.SpliceEventID != "" {
		fields["SpliceEventID"] = details.SpliceEventID
	}
	if details.SegmentationTypeID > 0 {
		fields["SegmentationTypeID"] = fmt.Sprintf("0x%02x", details.SegmentationTypeID)
	}
	return fields
}
