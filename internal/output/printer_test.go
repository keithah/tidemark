package output

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keithah/tidemark/internal/marker"
)

func testMarker() *marker.Marker {
	return &marker.Marker{
		Type:           marker.MarkerICY,
		Classification: marker.AdStart,
		Source:         "icy_stream",
		Fields:        map[string]string{"StreamTitle": "Ad Break"},
		Timestamp:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func TestPrintDefaultMode(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	err := Print(&buf, m, OutputConfig{Mode: ModeDefault, NoColor: true})
	if err != nil {
		t.Fatalf("Print error: %v", err)
	}
	out := buf.String()
	// Should contain JSON block
	if !strings.Contains(out, "{") {
		t.Error("default mode should contain JSON block")
	}
	// Should contain summary line
	if !strings.Contains(out, "▶") {
		t.Error("default mode should contain summary line")
	}
}

func TestPrintDefaultModeWithColor(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	err := Print(&buf, m, OutputConfig{Mode: ModeDefault, NoColor: false})
	if err != nil {
		t.Fatalf("Print error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\033[") {
		t.Error("colored mode should contain ANSI codes")
	}
}

func TestPrintDefaultModeNoColor(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	err := Print(&buf, m, OutputConfig{Mode: ModeDefault, NoColor: true})
	if err != nil {
		t.Fatalf("Print error: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Error("noColor mode should not contain ANSI codes")
	}
}

func TestPrintModeJSON(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	err := Print(&buf, m, OutputConfig{Mode: ModeJSON})
	if err != nil {
		t.Fatalf("Print error: %v", err)
	}
	out := buf.String()
	// Should be a single line of compact JSON
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Errorf("JSON mode should produce 1 line, got %d", len(lines))
	}
	// Should be valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Errorf("JSON mode output is not valid JSON: %v", err)
	}
}

func TestPrintModeJSONNoSummaryLine(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	_ = Print(&buf, m, OutputConfig{Mode: ModeJSON})
	if strings.Contains(buf.String(), "▶") {
		t.Error("JSON mode should not contain summary line")
	}
}

func TestPrintModeQuiet(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	err := Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: true})
	if err != nil {
		t.Fatalf("Print error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "▶") {
		t.Error("quiet mode should contain summary line")
	}
}

func TestPrintModeQuietNoJSON(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: true})
	if strings.Contains(buf.String(), "{") {
		t.Error("quiet mode should not contain JSON block")
	}
}

func TestPrintSCTE35Summary(t *testing.T) {
	var buf bytes.Buffer
	m := &marker.Marker{
		Type:           marker.MarkerSCTE35,
		Classification: marker.AdStart,
		Source:         "hls_manifest",
		PTS:           38113.135,
		Segment:       42,
		Fields:        map[string]string{"CommandName": "Splice Insert", "BreakDuration": "90.023"},
		Timestamp:      time.Now(),
	}
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: true})
	out := buf.String()
	if !strings.Contains(out, "SCTE35") {
		t.Error("summary should contain SCTE35")
	}
	if !strings.Contains(out, "Splice Insert") {
		t.Error("summary should contain command name")
	}
}

func TestPrintID3Summary(t *testing.T) {
	var buf bytes.Buffer
	m := &marker.Marker{
		Type:           marker.MarkerID3,
		Classification: marker.AdStart,
		Source:         "hls_segment",
		Tags:          map[string]string{"TIT2": "Ad Break"},
		Timestamp:      time.Now(),
	}
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: true})
	out := buf.String()
	if !strings.Contains(out, "ID3") {
		t.Error("summary should contain ID3")
	}
}

func TestPrintColorGreen(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker() // AD_START
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: false})
	if !strings.Contains(buf.String(), colorGreen) {
		t.Error("AD_START should use green color")
	}
}

func TestPrintColorYellow(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	m.Classification = marker.AdEnd
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: false})
	if !strings.Contains(buf.String(), colorYellow) {
		t.Error("AD_END should use yellow color")
	}
}

func TestPrintColorGray(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	m.Classification = marker.Unknown
	_ = Print(&buf, m, OutputConfig{Mode: ModeQuiet, NoColor: false})
	if !strings.Contains(buf.String(), colorGray) {
		t.Error("UNKNOWN should use gray color")
	}
}

func TestPrintJSONFields(t *testing.T) {
	var buf bytes.Buffer
	m := testMarker()
	_ = Print(&buf, m, OutputConfig{Mode: ModeJSON})
	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// Check that MarkerType and Classification are strings (custom MarshalJSON)
	if typ, ok := parsed["Type"].(string); !ok || typ != "ICY" {
		t.Errorf("Type should be 'ICY', got %v", parsed["Type"])
	}
	if cls, ok := parsed["Classification"].(string); !ok || cls != "AD_START" {
		t.Errorf("Classification should be 'AD_START', got %v", parsed["Classification"])
	}
}

// --- NDJSON tests ---

func TestJSONOutWriteAndReadback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ndjson")
	jout, err := NewJSONOut(path)
	if err != nil {
		t.Fatalf("NewJSONOut: %v", err)
	}

	markers := []*marker.Marker{
		{Type: marker.MarkerICY, Classification: marker.AdStart, Source: "icy_stream", Timestamp: time.Now()},
		{Type: marker.MarkerSCTE35, Classification: marker.AdEnd, Source: "hls_manifest", Timestamp: time.Now()},
		{Type: marker.MarkerID3, Classification: marker.Unknown, Source: "hls_segment", Timestamp: time.Now()},
	}
	for _, m := range markers {
		if err := jout.Write(m); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	jout.Close()

	// Read back and verify
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	count := 0
	for sc.Scan() {
		var parsed map[string]interface{}
		if err := json.Unmarshal(sc.Bytes(), &parsed); err != nil {
			t.Errorf("line %d invalid JSON: %v", count, err)
		}
		count++
	}
	if count != 3 {
		t.Errorf("expected 3 lines, got %d", count)
	}
}

func TestJSONOutReadbackFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ndjson")
	jout, err := NewJSONOut(path)
	if err != nil {
		t.Fatalf("NewJSONOut: %v", err)
	}

	m := &marker.Marker{
		Type:           marker.MarkerICY,
		Classification: marker.AdStart,
		Source:         "icy_stream",
		Fields:        map[string]string{"StreamTitle": "Ad Break"},
		Timestamp:      time.Now(),
	}
	_ = jout.Write(m)
	jout.Close()

	data, _ := os.ReadFile(path)
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if parsed["Type"] != "ICY" {
		t.Errorf("Type = %v, want ICY", parsed["Type"])
	}
	if parsed["Classification"] != "AD_START" {
		t.Errorf("Classification = %v, want AD_START", parsed["Classification"])
	}
}

func TestJSONOutClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ndjson")
	jout, err := NewJSONOut(path)
	if err != nil {
		t.Fatalf("NewJSONOut: %v", err)
	}
	if err := jout.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestJSONOutCreateError(t *testing.T) {
	_, err := NewJSONOut("/nonexistent/path/test.ndjson")
	if err == nil {
		t.Fatal("expected error for bad path")
	}
}

func TestJSONOutTruncatesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.ndjson")
	// Write some data
	os.WriteFile(path, []byte("old data\n"), 0644)

	jout, err := NewJSONOut(path)
	if err != nil {
		t.Fatalf("NewJSONOut: %v", err)
	}
	m := &marker.Marker{Type: marker.MarkerICY, Source: "test", Timestamp: time.Now()}
	_ = jout.Write(m)
	jout.Close()

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "old data") {
		t.Error("should have truncated existing file")
	}
}
