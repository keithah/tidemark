package scte35

import (
	"testing"

	"github.com/keithah/tidemark/internal/marker"
)

func TestDetailsFieldsDerivesStableOutputFields(t *testing.T) {
	oon := true
	details := marker.SCTE35Details{
		CommandName:           "Splice Insert",
		OutOfNetworkIndicator: &oon,
		BreakDuration:         30,
		SpliceEventID:         "0x2a",
		SegmentationTypeID:    0x34,
	}

	fields := detailsFields(details)
	if fields["CommandName"] != "Splice Insert" {
		t.Fatalf("CommandName = %q, want Splice Insert", fields["CommandName"])
	}
	if fields["OutOfNetworkIndicator"] != "true" {
		t.Fatalf("OutOfNetworkIndicator = %q, want true", fields["OutOfNetworkIndicator"])
	}
	if fields["BreakDuration"] != "30.000" {
		t.Fatalf("BreakDuration = %q, want 30.000", fields["BreakDuration"])
	}
	if fields["SpliceEventID"] != "0x2a" {
		t.Fatalf("SpliceEventID = %q, want 0x2a", fields["SpliceEventID"])
	}
	if fields["SegmentationTypeID"] != "0x34" {
		t.Fatalf("SegmentationTypeID = %q, want 0x34", fields["SegmentationTypeID"])
	}
}
