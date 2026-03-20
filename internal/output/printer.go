package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/keithah/tidemark/internal/marker"
)

// Output modes
const (
	ModeDefault = iota // JSON block + summary line
	ModeJSON           // compact JSON only
	ModeQuiet          // summary line only
)

// OutputConfig controls output formatting.
type OutputConfig struct {
	Mode    int
	NoColor bool
}

// ANSI color codes
const (
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorGray   = "\033[90m"
	colorReset  = "\033[0m"
)

// Print writes a marker to the writer according to the output config.
func Print(w io.Writer, m *marker.Marker, cfg OutputConfig) error {
	switch cfg.Mode {
	case ModeJSON:
		return printJSON(w, m)
	case ModeQuiet:
		return printSummary(w, m, cfg.NoColor)
	default:
		if err := printJSONBlock(w, m); err != nil {
			return err
		}
		return printSummary(w, m, cfg.NoColor)
	}
}

func printJSONBlock(w io.Writer, m *marker.Marker) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func printJSON(w io.Writer, m *marker.Marker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func printSummary(w io.Writer, m *marker.Marker, noColor bool) error {
	color := colorForClassification(m.Classification)
	reset := colorReset
	if noColor {
		color = ""
		reset = ""
	}

	detail := summaryDetail(m)
	_, err := fmt.Fprintf(w, "%s▶%s [%-6s] %-9s  %s\n",
		color, reset,
		m.Type.String(),
		m.Classification.String(),
		detail,
	)
	return err
}

func colorForClassification(c marker.Classification) string {
	switch c {
	case marker.AdStart:
		return colorGreen
	case marker.AdEnd:
		return colorYellow
	default:
		return colorGray
	}
}

func summaryDetail(m *marker.Marker) string {
	switch m.Type {
	case marker.MarkerICY:
		if title, ok := m.Fields["StreamTitle"]; ok {
			return "StreamTitle=" + title
		}
		return ""
	case marker.MarkerSCTE35:
		var parts []string
		if name, ok := m.Fields["CommandName"]; ok {
			parts = append(parts, name)
		}
		if dur, ok := m.Fields["BreakDuration"]; ok {
			parts = append(parts, "break="+dur+"s")
		}
		if pts := m.PTS; pts > 0 {
			parts = append(parts, fmt.Sprintf("pts=%.3f", pts))
		}
		if m.Segment > 0 {
			parts = append(parts, fmt.Sprintf("seg=%d", m.Segment))
		}
		return strings.Join(parts, "  ")
	case marker.MarkerID3:
		var parts []string
		for k, v := range m.Tags {
			parts = append(parts, k+"="+v)
		}
		return strings.Join(parts, "  ")
	default:
		return ""
	}
}
