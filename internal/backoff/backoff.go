package backoff

import "time"

// Next advances an exponential backoff delay from a default starting value.
func Next(delay, defaultDelay, maxDelay time.Duration) time.Duration {
	if delay <= 0 {
		return defaultDelay
	}
	delay *= 2
	if maxDelay > 0 && delay > maxDelay {
		return maxDelay
	}
	return delay
}

// WithJitter adds up to 50% positive jitter using the supplied random source.
func WithJitter(delay time.Duration, randInt63n func(int64) int64) time.Duration {
	jitter := delay / 2
	if jitter <= 0 {
		return delay
	}
	return delay + time.Duration(randInt63n(int64(jitter)))
}
