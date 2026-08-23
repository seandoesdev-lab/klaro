package explorer

import (
	"testing"
	"time"
)

var now = time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

func TestClampNarrowsToThePlanWindow(t *testing.T) {
	// Asking for a week on a plan that keeps a day gets a day.
	got, clamped := clamp(TimeRange{From: now.Add(-7 * 24 * time.Hour), To: now}, 24*time.Hour, now)
	if !clamped {
		t.Fatal("a window past retention was not clamped")
	}
	if !got.From.Equal(now.Add(-24 * time.Hour)) {
		t.Errorf("from = %v, want the retention edge", got.From)
	}
	if !got.To.Equal(now) {
		t.Errorf("to moved: %v", got.To)
	}
}

func TestClampLeavesAWindowInsideRetentionAlone(t *testing.T) {
	in := TimeRange{From: now.Add(-time.Hour), To: now}
	got, clamped := clamp(in, 24*time.Hour, now)
	if clamped || !got.From.Equal(in.From) {
		t.Errorf("clamped a window that fits: %v", got)
	}
}

// Zero retention means unlimited. A missing plan row must never read as an
// instruction to hide a customer's data.
func TestClampTreatsZeroAsUnlimited(t *testing.T) {
	in := TimeRange{From: time.Unix(0, 0), To: now}
	got, clamped := clamp(in, 0, now)
	if clamped || !got.From.Equal(in.From) {
		t.Errorf("zero retention narrowed the window: %v", got)
	}
}

func TestPickResolution(t *testing.T) {
	keep := Retention{Metrics: 24 * time.Hour, Rollup: 90 * 24 * time.Hour}

	cases := []struct {
		name string
		from time.Time
		step time.Duration
		want string
	}{
		{"inside raw retention", now.Add(-time.Hour), time.Minute, ResolutionRaw},
		{"exactly at the raw edge", now.Add(-24 * time.Hour), time.Minute, ResolutionRaw},
		{"past raw, fine step", now.Add(-48 * time.Hour), time.Minute, Resolution5m},
		// A coarse step means the caller is drawing an hourly chart; reading 5m
		// buckets for it is twelve times the work for the same picture.
		{"past raw, hourly step", now.Add(-48 * time.Hour), time.Hour, Resolution1h},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickResolution(tc.from, tc.step, keep, now); got != tc.want {
				t.Errorf("resolution = %q, want %q", got, tc.want)
			}
		})
	}
}

// Without rollups on the plan there is nothing to fall back to, so the answer
// stays raw and the window is narrowed instead.
func TestPickResolutionWithoutRollups(t *testing.T) {
	keep := Retention{Metrics: 24 * time.Hour}
	if got := pickResolution(now.Add(-48*time.Hour), time.Minute, keep, now); got != ResolutionRaw {
		t.Errorf("resolution = %q, want raw", got)
	}
}

func TestPickResolutionWithUnlimitedRetention(t *testing.T) {
	if got := pickResolution(time.Unix(0, 0), time.Minute, Retention{}, now); got != ResolutionRaw {
		t.Errorf("resolution = %q, want raw", got)
	}
}

func TestRollupSeries(t *testing.T) {
	if rollupSeries(Resolution5m) != Rollup5m || rollupSeries(Resolution1h) != Rollup1h {
		t.Error("rollup series names do not match their resolutions")
	}
	if rollupSeries(ResolutionRaw) != "" {
		t.Error("raw mapped to a rollup series")
	}
}
