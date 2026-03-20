package hls

import (
	"testing"

	"github.com/keithah/tidemark/internal/marker"
)

func TestParseSCTE35Tag(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantTag string
		wantPay string
	}{
		{"with CUE=", "#EXT-X-SCTE35:CUE=/DARAAAAAAAAAP/wAAAAAA==", "#EXT-X-SCTE35", "/DARAAAAAAAAAP/wAAAAAA=="},
		{"raw payload", "#EXT-X-SCTE35:/DARAAAAAAAAAP/wAAAAAA==", "#EXT-X-SCTE35", "/DARAAAAAAAAAP/wAAAAAA=="},
		{"empty", "#EXT-X-SCTE35:", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ParseLine(tt.line)
			if tt.wantTag == "" {
				if r != nil {
					t.Error("expected nil result")
				}
				return
			}
			if r == nil {
				t.Fatal("expected non-nil result")
			}
			if r.Tag != tt.wantTag {
				t.Errorf("Tag = %q, want %q", r.Tag, tt.wantTag)
			}
			if r.Payload != tt.wantPay {
				t.Errorf("Payload = %q, want %q", r.Payload, tt.wantPay)
			}
		})
	}
}

func TestParseOATCLSTag(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantNil bool
	}{
		{"valid", "#EXT-OATCLS-SCTE35:/DARAAAAAAAAAP/wAAAAAA==", false},
		{"empty", "#EXT-OATCLS-SCTE35:", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ParseLine(tt.line)
			if tt.wantNil && r != nil {
				t.Error("expected nil")
			}
			if !tt.wantNil && r == nil {
				t.Fatal("expected non-nil")
			}
			if !tt.wantNil && r.Tag != "#EXT-OATCLS-SCTE35" {
				t.Errorf("Tag = %q", r.Tag)
			}
		})
	}
}

func TestParseDateRangeTag(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		wantNil bool
		wantPay string
	}{
		{
			"SCTE35-OUT",
			`#EXT-X-DATERANGE:ID="splice-1",START-DATE="2026-01-01",SCTE35-OUT=0xFC301100`,
			false,
			"0xFC301100",
		},
		{
			"SCTE35-IN",
			`#EXT-X-DATERANGE:ID="splice-1",START-DATE="2026-01-01",SCTE35-IN=0xFC301100`,
			false,
			"0xFC301100",
		},
		{
			"no SCTE35",
			`#EXT-X-DATERANGE:ID="date-1",START-DATE="2026-01-01",DURATION=30.0`,
			true,
			"",
		},
		{
			"quoted comma",
			`#EXT-X-DATERANGE:ID="a,b",SCTE35-OUT=AAAA`,
			false,
			"AAAA",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ParseLine(tt.line)
			if tt.wantNil {
				if r != nil {
					t.Error("expected nil")
				}
				return
			}
			if r == nil {
				t.Fatal("expected non-nil")
			}
			if r.Payload != tt.wantPay {
				t.Errorf("Payload = %q, want %q", r.Payload, tt.wantPay)
			}
		})
	}
}

func TestParseCueOutTag(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"bare", "#EXT-X-CUE-OUT"},
		{"with duration", "#EXT-X-CUE-OUT:DURATION=30"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ParseLine(tt.line)
			if r == nil {
				t.Fatal("expected non-nil")
			}
			if !r.IsDirect {
				t.Error("expected IsDirect=true")
			}
			if r.DirectType != marker.AdStart {
				t.Errorf("DirectType = %v, want AdStart", r.DirectType)
			}
		})
	}
}

func TestParseCueInTag(t *testing.T) {
	r := ParseLine("#EXT-X-CUE-IN")
	if r == nil {
		t.Fatal("expected non-nil")
	}
	if !r.IsDirect {
		t.Error("expected IsDirect=true")
	}
	if r.DirectType != marker.AdEnd {
		t.Errorf("DirectType = %v, want AdEnd", r.DirectType)
	}
}

func TestParseLineNonSCTE35(t *testing.T) {
	tests := []string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:6",
		"#EXTINF:6.0,",
		"segment001.ts",
		"",
	}
	for _, line := range tests {
		r := ParseLine(line)
		if r != nil {
			t.Errorf("ParseLine(%q) should return nil", line)
		}
	}
}

func TestParseAttributes(t *testing.T) {
	attrs := parseAttributes(`ID="test",DURATION=30.0,TITLE="Hello, World"`)
	if attrs["ID"] != "test" {
		t.Errorf("ID = %q", attrs["ID"])
	}
	if attrs["DURATION"] != "30.0" {
		t.Errorf("DURATION = %q", attrs["DURATION"])
	}
	if attrs["TITLE"] != "Hello, World" {
		t.Errorf("TITLE = %q, want 'Hello, World'", attrs["TITLE"])
	}
}
