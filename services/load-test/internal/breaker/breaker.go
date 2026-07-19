package breaker

import (
	"fmt"
	"time"
)

type sample struct {
	ts time.Time
	ok bool
}

// Breaker computes a sliding-window error rate and trips when the rate exceeds
// a threshold once enough samples are present.
type Breaker struct {
	window     time.Duration
	threshold  float64
	minSamples int
	samples    []sample
}

func New(window time.Duration, threshold float64, minSamples int) *Breaker {
	return &Breaker{window: window, threshold: threshold, minSamples: minSamples}
}

func (b *Breaker) Observe(ts time.Time, ok bool) {
	b.samples = append(b.samples, sample{ts: ts, ok: ok})
}

func (b *Breaker) evict(now time.Time) {
	cutoff := now.Add(-b.window)
	i := 0
	for i < len(b.samples) && b.samples[i].ts.Before(cutoff) {
		i++
	}
	if i > 0 {
		b.samples = b.samples[i:]
	}
}

// ShouldAbort returns true with a reason when the windowed error rate exceeds
// the threshold and at least minSamples are present.
func (b *Breaker) ShouldAbort(now time.Time) (bool, string) {
	b.evict(now)
	if len(b.samples) < b.minSamples {
		return false, ""
	}
	errCount := 0
	for _, s := range b.samples {
		if !s.ok {
			errCount++
		}
	}
	rate := float64(errCount) / float64(len(b.samples))
	if rate > b.threshold {
		return true, fmt.Sprintf("error_rate > %.2f (observed %.2f)", b.threshold, rate)
	}
	return false, ""
}
