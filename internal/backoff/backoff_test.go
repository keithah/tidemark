package backoff

import (
	"testing"
	"time"
)

func TestNextUsesDefaultForNonPositiveDelay(t *testing.T) {
	if got := Next(0, time.Second, 30*time.Second); got != time.Second {
		t.Fatalf("Next = %s, want 1s", got)
	}
}

func TestNextDoublesAndCapsDelay(t *testing.T) {
	if got := Next(2*time.Second, time.Second, 30*time.Second); got != 4*time.Second {
		t.Fatalf("Next = %s, want 4s", got)
	}
	if got := Next(20*time.Second, time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("Next = %s, want 30s", got)
	}
}

func TestWithJitterBoundsDelay(t *testing.T) {
	got := WithJitter(10*time.Second, func(n int64) int64 {
		if n != int64(5*time.Second) {
			t.Fatalf("jitter bound = %d, want %d", n, int64(5*time.Second))
		}
		return int64(2 * time.Second)
	})
	if got != 12*time.Second {
		t.Fatalf("WithJitter = %s, want 12s", got)
	}
}

func TestWithJitterLeavesNonPositiveDelayUnchanged(t *testing.T) {
	if got := WithJitter(0, func(int64) int64 { return 1 }); got != 0 {
		t.Fatalf("WithJitter = %s, want 0", got)
	}
}
