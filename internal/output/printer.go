package output

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/keithah/tidemark/internal/marker"
)

// Output modes
const (
	ModeDefault = iota // compact table rows
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
		return printTableRows(w, m)
	}
}

// PrintHeader writes the default streaming table header.
func PrintHeader(w io.Writer, cfg OutputConfig) error {
	if cfg.Mode != ModeDefault {
		return nil
	}
	_, err := fmt.Fprintln(w, "TIME                  TYPE    CLASS      SEG      FRAME  VALUE")
	return err
}

func printJSON(w io.Writer, m *marker.Marker) error {
	enc := json.NewEncoder(w)
	if err := enc.Encode(m); err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return nil
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

func printTableRows(w io.Writer, m *marker.Marker) error {
	t := m.Timestamp
	if t.IsZero() {
		t = markerTimeNow()
	}
	stamp := t.Format("2006-01-02T15:04:05")
	seg := ""
	if m.Segment > 0 {
		seg = fmt.Sprintf("%d", m.Segment)
	}

	rows := tableRows(m)
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "%-19s  %-6s  %-9s  %-7s  %-5s  %s\n",
			stamp,
			m.Type.String(),
			m.Classification.String(),
			seg,
			cleanSummaryText(row.frame),
			cleanSummaryText(row.value),
		); err != nil {
			return err
		}
	}
	return nil
}

type tableRow struct {
	frame string
	value string
}

func tableRows(m *marker.Marker) []tableRow {
	switch m.Type {
	case marker.MarkerID3:
		keys := orderedTagKeys(m.Tags)
		rows := make([]tableRow, 0, len(keys))
		for _, key := range keys {
			rows = append(rows, tableRow{frame: key, value: m.Tags[key]})
		}
		return rows
	case marker.MarkerICY:
		if title, ok := m.Fields["StreamTitle"]; ok {
			return []tableRow{{frame: "ICY", value: title}}
		}
	case marker.MarkerSCTE35:
		frame := m.Tag
		if frame == "" {
			frame = "SCTE35"
		}
		return []tableRow{{frame: frame, value: summaryDetail(m)}}
	}
	return []tableRow{{value: summaryDetail(m)}}
}

func orderedTagKeys(tags map[string]string) []string {
	if len(tags) == 0 {
		return nil
	}

	preferred := []string{"TIT2", "TPE1", "TALB", "TDRC", "TLEN", "LINK", "TDTG"}
	seen := make(map[string]bool, len(tags))
	keys := make([]string, 0, len(tags))
	for _, key := range preferred {
		if _, ok := tags[key]; ok {
			keys = append(keys, key)
			seen[key] = true
		}
	}

	var rest []string
	for key := range tags {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

var markerTimeNow = func() time.Time {
	return time.Now()
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
			return "StreamTitle=" + cleanSummaryText(title)
		}
		return ""
	case marker.MarkerSCTE35:
		parts := make([]string, 0, 4)
		if name, ok := m.Fields["CommandName"]; ok {
			parts = append(parts, cleanSummaryText(name))
		}
		if dur, ok := m.Fields["BreakDuration"]; ok {
			parts = append(parts, "break="+cleanSummaryText(dur)+"s")
		}
		if pts := m.PTS; pts > 0 {
			parts = append(parts, fmt.Sprintf("pts=%.3f", pts))
		}
		if m.Segment > 0 {
			parts = append(parts, fmt.Sprintf("seg=%d", m.Segment))
		}
		return strings.Join(parts, "  ")
	case marker.MarkerID3:
		parts := make([]string, 0, len(m.Tags))
		for k, v := range m.Tags {
			parts = append(parts, cleanSummaryText(k)+"="+cleanSummaryText(v))
		}
		return strings.Join(parts, "  ")
	default:
		return ""
	}
}

func cleanSummaryText(text string) string {
	if text == "" {
		return ""
	}
	if !utf8.ValidString(text) {
		return "[binary data]"
	}

	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
