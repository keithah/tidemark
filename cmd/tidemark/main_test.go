package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keithah/tidemark/internal/classifier"
	"github.com/keithah/tidemark/internal/marker"
	"github.com/keithah/tidemark/internal/output"
)

func TestParseFlags_MutualExclusion(t *testing.T) {
	_, _, _, _, err := parseFlags([]string{"--json", "--quiet", "http://example.com"})
	if err == nil {
		t.Fatal("expected error for --json --quiet")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error = %q, want 'mutually exclusive'", err.Error())
	}
}

func TestParseFlags_FilterValidation(t *testing.T) {
	_, _, _, _, err := parseFlags([]string{"--filter", "invalid", "http://example.com"})
	if err == nil {
		t.Fatal("expected error for invalid filter")
	}
}

func TestParseFlags_FilterValid(t *testing.T) {
	for _, f := range []string{"scte35", "SCTE35", "icy", "ICY", "id3", "ID3"} {
		_, _, _, cancel, err := parseFlags([]string{"--filter", f, "http://example.com"})
		if err != nil {
			t.Errorf("filter %q should be valid, got error: %v", f, err)
		}
		cancel()
	}
}

func TestParseFlags_TimeoutSetsValue(t *testing.T) {
	cfg, _, _, cancel, err := parseFlags([]string{"--timeout", "30", "http://example.com"})
	defer cancel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Timeout != 30 {
		t.Errorf("Timeout = %d, want 30", cfg.Timeout)
	}
}

func TestParseFlags_TimeoutContextDeadline(t *testing.T) {
	_, _, ctx, cancel, err := parseFlags([]string{"--timeout", "1", "http://example.com"})
	defer cancel()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have deadline")
	}
	if time.Until(deadline) > 2*time.Second {
		t.Error("deadline should be within 2 seconds")
	}
}

// simulateMarkerLoop simulates the marker consumption loop for testing shutdown messages.
func simulateMarkerLoop(markers []*marker.Marker, filter string, noColor bool) (int, string) {
	cls := classifier.New()
	var stderr bytes.Buffer
	ocfg := output.OutputConfig{Mode: output.ModeQuiet, NoColor: noColor}

	markerCount := 0
	for _, m := range markers {
		m.Classification = cls.Classify(m)
		if shouldFilter(m, filter) {
			continue
		}
		markerCount++
		_ = output.Print(os.Stdout, m, ocfg)
	}

	// Write shutdown message
	(&stderr).WriteString("\n[tidemark] stopped. ")
	(&stderr).WriteString(strings.Replace(
		strings.Replace("[tidemark] stopped. N markers detected.\n", "N", itoa(markerCount), 1),
		"[tidemark] stopped. ", "", 1,
	))

	return markerCount, stderr.String()
}

func itoa(i int) string {
	return strings.TrimRight(strings.TrimLeft(
		strings.Replace("     ", " ", string(rune('0'+i)), 1), " "), " ")
}

func TestShutdownMessage_NoFilter(t *testing.T) {
	markers := []*marker.Marker{
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Song A"}},
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Ad Break"}},
		{Type: marker.MarkerSCTE35, Fields: map[string]string{"CommandName": "Splice Insert", "OutOfNetworkIndicator": "true"}},
	}
	count, _ := simulateMarkerLoop(markers, "", true)
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestShutdownMessage_WithFilter(t *testing.T) {
	markers := []*marker.Marker{
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Song A"}, Timestamp: time.Now()},
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Song B"}, Timestamp: time.Now()},
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Song C"}, Timestamp: time.Now()},
		{Type: marker.MarkerSCTE35, Fields: map[string]string{"CommandName": "Splice Insert", "OutOfNetworkIndicator": "true"}, Timestamp: time.Now()},
		{Type: marker.MarkerID3, Tags: map[string]string{"TIT2": "Track"}, Timestamp: time.Now()},
	}
	count, _ := simulateMarkerLoop(markers, "icy", true)
	if count != 3 {
		t.Errorf("count = %d, want 3 (ICY only)", count)
	}
}

func TestShutdownMessage_ZeroMarkers(t *testing.T) {
	count, _ := simulateMarkerLoop(nil, "", true)
	if count != 0 {
		t.Errorf("count = %d, want 0", count)
	}
}

func TestShutdownMessage_AllFiltered(t *testing.T) {
	markers := []*marker.Marker{
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Song"}, Timestamp: time.Now()},
		{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Other"}, Timestamp: time.Now()},
	}
	count, _ := simulateMarkerLoop(markers, "scte35", true)
	if count != 0 {
		t.Errorf("count = %d, want 0 (all filtered)", count)
	}
}

func TestShutdownMessage_WithJSONOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "markers.ndjson")
	jout, err := output.NewJSONOut(path)
	if err != nil {
		t.Fatalf("NewJSONOut: %v", err)
	}

	m := &marker.Marker{
		Type:           marker.MarkerICY,
		Classification: marker.AdStart,
		Source:         "icy_stream",
		Fields:        map[string]string{"StreamTitle": "Ad"},
		Timestamp:      time.Now(),
	}
	_ = jout.Write(m)
	jout.Close()

	data, _ := os.ReadFile(path)
	if len(data) == 0 {
		t.Error("expected NDJSON output")
	}
}

func TestShouldFilter(t *testing.T) {
	tests := []struct {
		markerType marker.MarkerType
		filter     string
		want       bool
	}{
		{marker.MarkerICY, "", false},
		{marker.MarkerICY, "icy", false},
		{marker.MarkerICY, "scte35", true},
		{marker.MarkerSCTE35, "scte35", false},
		{marker.MarkerID3, "id3", false},
		{marker.MarkerID3, "icy", true},
	}
	for _, tt := range tests {
		m := &marker.Marker{Type: tt.markerType}
		got := shouldFilter(m, tt.filter)
		if got != tt.want {
			t.Errorf("shouldFilter(%v, %q) = %v, want %v", tt.markerType, tt.filter, got, tt.want)
		}
	}
}

func TestPrintBanner_DefaultConfig(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{}
	printBanner(&buf, "http://example.com/stream", marker.StreamICY, cfg)
	out := buf.String()
	if !strings.Contains(out, "http://example.com/stream") {
		t.Error("banner should contain URL")
	}
	if !strings.Contains(out, "ICY") {
		t.Error("banner should contain stream type")
	}
	if !strings.Contains(out, "filter: all") {
		t.Error("banner should show filter: all")
	}
	if !strings.Contains(out, "output: default") {
		t.Error("banner should show output: default")
	}
}

func TestPrintBanner_JSONMode(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{JSON: true}
	printBanner(&buf, "http://example.com", marker.StreamHLS, cfg)
	if !strings.Contains(buf.String(), "output: json") {
		t.Error("banner should show output: json")
	}
}

func TestPrintBanner_QuietMode(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{Quiet: true, Filter: "scte35"}
	printBanner(&buf, "http://example.com", marker.StreamHLS, cfg)
	if !strings.Contains(buf.String(), "output: quiet") {
		t.Error("banner should show output: quiet")
	}
	if !strings.Contains(buf.String(), "filter: scte35") {
		t.Error("banner should show filter: scte35")
	}
}

func TestPrintBanner_WithJSONOut(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{JSONOut: "/tmp/markers.ndjson"}
	printBanner(&buf, "http://example.com", marker.StreamICY, cfg)
	if !strings.Contains(buf.String(), "json-out: /tmp/markers.ndjson") {
		t.Error("banner should show json-out path")
	}
}

func TestPrintBanner_Separator(t *testing.T) {
	var buf bytes.Buffer
	cfg := &Config{}
	printBanner(&buf, "http://example.com", marker.StreamICY, cfg)
	if !strings.Contains(buf.String(), "─────") {
		t.Error("banner should end with separator line")
	}
}

func TestOutputConfig(t *testing.T) {
	tests := []struct {
		cfg  *Config
		want int
	}{
		{&Config{}, output.ModeDefault},
		{&Config{JSON: true}, output.ModeJSON},
		{&Config{Quiet: true}, output.ModeQuiet},
	}
	for _, tt := range tests {
		got := outputConfig(tt.cfg)
		if got.Mode != tt.want {
			t.Errorf("outputConfig mode = %d, want %d", got.Mode, tt.want)
		}
	}
}
