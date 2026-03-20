package classifier

import (
	"testing"
	"github.com/keithah/tidemark/internal/marker"
)

func TestClassifyICYAdStart(t *testing.T) {
	c := New()
	tests := []struct {
		title string
		want  marker.Classification
	}{
		{"Pandora Spot | Artist: Tide", marker.AdStart},
		{"Ad Break Now", marker.AdStart},
		{"This is a PROMO message", marker.AdStart},
		{"COMMERCIAL break", marker.AdStart},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			c.inAd = false // reset state
			m := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": tt.title}}
			got := c.Classify(m)
			if got != tt.want {
				t.Errorf("Classify(%q) = %s, want %s", tt.title, got, tt.want)
			}
		})
	}
}

func TestClassifyICYUnknown(t *testing.T) {
	c := New()
	m := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "My Favorite Song"}}
	got := c.Classify(m)
	if got != marker.Unknown {
		t.Errorf("expected UNKNOWN, got %s", got)
	}
}

func TestClassifyICYAdEnd(t *testing.T) {
	c := New()
	// First, see an ad
	ad := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Ad Break"}}
	c.Classify(ad) // sets inAd = true

	// Then see a normal title
	normal := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "Back to Music"}}
	got := c.Classify(normal)
	if got != marker.AdEnd {
		t.Errorf("expected AD_END after ad, got %s", got)
	}
}

func TestClassifyICYFullCycle(t *testing.T) {
	c := New()

	steps := []struct {
		title string
		want  marker.Classification
	}{
		{"Regular Song", marker.Unknown},
		{"Ad Spot Here", marker.AdStart},
		{"Normal Music", marker.AdEnd},
		{"Another Song", marker.Unknown},
		{"Commercial Break", marker.AdStart},
		{"Regular Programming", marker.AdEnd},
	}
	for _, s := range steps {
		m := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": s.title}}
		got := c.Classify(m)
		if got != s.want {
			t.Errorf("Classify(%q) = %s, want %s", s.title, got, s.want)
		}
	}
}

func TestClassifyICYWordBoundary(t *testing.T) {
	c := New()
	// "administer" contains "ad" but should not match at word boundary
	m := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "The Administrator"}}
	got := c.Classify(m)
	if got != marker.Unknown {
		t.Errorf("expected UNKNOWN for word boundary, got %s", got)
	}
}

func TestClassifyICYCaseInsensitive(t *testing.T) {
	c := New()
	m := &marker.Marker{Type: marker.MarkerICY, Fields: map[string]string{"StreamTitle": "SPOT break"}}
	got := c.Classify(m)
	if got != marker.AdStart {
		t.Errorf("expected AD_START for case-insensitive match, got %s", got)
	}
}

func TestClassifySCTE35SpliceInsertOON(t *testing.T) {
	m := &marker.Marker{
		Type:   marker.MarkerSCTE35,
		Fields: map[string]string{"CommandName": "Splice Insert", "OutOfNetworkIndicator": "true"},
	}
	c := New()
	got := c.Classify(m)
	if got != marker.AdStart {
		t.Errorf("expected AD_START for SpliceInsert OON=true, got %s", got)
	}
}

func TestClassifySCTE35SpliceInsertReturn(t *testing.T) {
	m := &marker.Marker{
		Type:   marker.MarkerSCTE35,
		Fields: map[string]string{"CommandName": "Splice Insert", "OutOfNetworkIndicator": "false"},
	}
	c := New()
	got := c.Classify(m)
	if got != marker.AdEnd {
		t.Errorf("expected AD_END for SpliceInsert OON=false, got %s", got)
	}
}

func TestClassifySCTE35TimeSignal(t *testing.T) {
	tests := []struct {
		name    string
		segType string
		want    marker.Classification
	}{
		{"ad start 0x22", "0x22", marker.AdStart},
		{"ad start 0x30", "0x30", marker.AdStart},
		{"ad start 0x34", "0x34", marker.AdStart},
		{"ad end 0x23", "0x23", marker.AdEnd},
		{"ad end 0x31", "0x31", marker.AdEnd},
		{"ad end 0x35", "0x35", marker.AdEnd},
		{"unknown type", "0x99", marker.Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &marker.Marker{
				Type:   marker.MarkerSCTE35,
				Fields: map[string]string{"CommandName": "Time Signal", "SegmentationTypeID": tt.segType},
			}
			c := New()
			got := c.Classify(m)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClassifySCTE35SpliceNull(t *testing.T) {
	m := &marker.Marker{
		Type:   marker.MarkerSCTE35,
		Fields: map[string]string{"CommandName": "Splice Null"},
	}
	c := New()
	got := c.Classify(m)
	if got != marker.Unknown {
		t.Errorf("expected UNKNOWN for SpliceNull, got %s", got)
	}
}

func TestClassifySCTE35NilFields(t *testing.T) {
	m := &marker.Marker{Type: marker.MarkerSCTE35}
	c := New()
	got := c.Classify(m)
	if got != marker.Unknown {
		t.Errorf("expected UNKNOWN for nil fields, got %s", got)
	}
}

func TestClassifyID3AdStart(t *testing.T) {
	tests := []struct {
		name  string
		tags  map[string]string
		want  marker.Classification
	}{
		{"TXXX with ad", map[string]string{"TXXX": "ad_id:abc123"}, marker.AdStart},
		{"TIT2 with spot", map[string]string{"TIT2": "Spot Break"}, marker.AdStart},
		{"TIT2 with promo", map[string]string{"TIT2": "Station Promo"}, marker.AdStart},
		{"TIT2 with commercial", map[string]string{"TIT2": "Commercial"}, marker.AdStart},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &marker.Marker{Type: marker.MarkerID3, Tags: tt.tags}
			c := New()
			got := c.Classify(m)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClassifyID3AdEnd(t *testing.T) {
	tests := []struct {
		name string
		tags map[string]string
		want marker.Classification
	}{
		{"ad_end", map[string]string{"TXXX": "ad_end"}, marker.AdEnd},
		{"content_start", map[string]string{"TXXX": "content_start"}, marker.AdEnd},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &marker.Marker{Type: marker.MarkerID3, Tags: tt.tags}
			c := New()
			got := c.Classify(m)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClassifyID3Unknown(t *testing.T) {
	m := &marker.Marker{Type: marker.MarkerID3, Tags: map[string]string{"TIT2": "My Favorite Song"}}
	c := New()
	got := c.Classify(m)
	if got != marker.Unknown {
		t.Errorf("expected UNKNOWN, got %s", got)
	}
}

func TestClassifyID3CaseInsensitive(t *testing.T) {
	m := &marker.Marker{Type: marker.MarkerID3, Tags: map[string]string{"TIT2": "AD BREAK"}}
	c := New()
	got := c.Classify(m)
	if got != marker.AdStart {
		t.Errorf("expected AD_START for case insensitive, got %s", got)
	}
}
