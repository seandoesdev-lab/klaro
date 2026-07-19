package breaker

import (
	"testing"
	"time"
)

func TestBreakerTripsOnHighErrorRate(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false) // all errors
	}
	trip, reason := b.ShouldAbort(base.Add(2 * time.Second))
	if !trip {
		t.Fatal("expected breaker to trip")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func TestBreakerHoldsUnderThreshold(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), i%2 == 0) // 50% error
	}
	if trip, _ := b.ShouldAbort(base.Add(2 * time.Second)); trip {
		t.Fatal("did not expect trip at 50% error")
	}
}

func TestBreakerWaitsForMinSamples(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false)
	}
	if trip, _ := b.ShouldAbort(base.Add(time.Second)); trip {
		t.Fatal("should not trip below minSamples")
	}
}

func TestBreakerEvictsOldSamples(t *testing.T) {
	b := New(10*time.Second, 0.8, 10)
	base := time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		b.Observe(base.Add(time.Duration(i)*100*time.Millisecond), false)
	}
	// 30s later the old error samples fall out of the window
	if trip, _ := b.ShouldAbort(base.Add(30 * time.Second)); trip {
		t.Fatal("old samples should have been evicted")
	}
}
