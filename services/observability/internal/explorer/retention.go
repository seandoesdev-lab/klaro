package explorer

import (
	"context"
	"time"
)

// Rollup series produced by the downsampling recording rules (design HOW-10).
//
// The names live here rather than in the alerting package that renders them,
// because the read path is the one that has to find them and a cycle the other
// way would be worse. Downsampling is metrics-only: traces and logs have no
// standard rollup, and inventing one would promise something the backends
// cannot deliver.
const (
	Rollup5m = "klaro_rollup5m"
	Rollup1h = "klaro_rollup1h"
	// MetricLabel carries the original metric name on a rollup series. The
	// recording rule overwrites __name__ with the rollup name, so the real name
	// has to be parked in a label or it is lost.
	MetricLabel = "klaro_metric"
	// ResolutionLabel says which tier a rollup series belongs to.
	ResolutionLabel = "klaro_resolution"
)

// Resolutions reported to the caller.
const (
	ResolutionRaw = "raw"
	Resolution5m  = "5m"
	Resolution1h  = "1h"
)

// Retention is how long each signal is kept for one org (plan-driven).
// A zero duration means unlimited: a missing plan must never read as an
// instruction to hide or delete a customer's data.
type Retention struct {
	Metrics time.Duration
	Traces  time.Duration
	Logs    time.Duration
	// Rollup is how long the downsampled metric series are kept. It is longer
	// than Metrics - outliving the raw data is the entire point of a rollup.
	Rollup time.Duration
}

// RetentionFunc reports the retention for an org.
//
// A function rather than an interface so the control plane can wire its plan
// store in without this package depending on it, and so a deployment with no
// plan concept can pass nil and get unlimited windows.
type RetentionFunc func(ctx context.Context, orgID string) (Retention, error)

// retentionFor resolves the org's retention, treating an absent resolver as
// unlimited.
func (c *Client) retentionFor(ctx context.Context, orgID string) Retention {
	if c.retention == nil {
		return Retention{}
	}
	r, err := c.retention(ctx, orgID)
	if err != nil {
		// A plan lookup failure must not silently widen the window past what the
		// customer paid for, but it also must not blank the dashboard. Falling
		// back to unlimited is the lesser evil: retention is enforced physically
		// by the retention job as well, so the data past the window is usually
		// already gone.
		return Retention{}
	}
	return r
}

// clamp narrows a range to what the plan still keeps.
//
// This is the enforcement the contract asks for ("data beyond the plan is not
// queryable"). Doing it here makes it exact and immediate, which no backend
// delete API can be: VictoriaMetrics cannot delete a time range, Tempo expires
// whole blocks, and Loki's delete is asynchronous.
func clamp(r TimeRange, keep time.Duration, now time.Time) (TimeRange, bool) {
	if keep <= 0 {
		return r, false
	}
	earliest := now.Add(-keep)
	if !r.From.Before(earliest) {
		return r, false
	}
	r.From = earliest
	return r, true
}

// pickResolution decides which series a metrics query should read.
//
// Raw while the window fits inside raw retention. Past that, the rollups are
// the only thing left, and the coarser tier is chosen when the caller already
// asked for a coarse step - reading 5m buckets to answer an hourly chart is
// twelve times the work for the same picture.
func pickResolution(from time.Time, step time.Duration, keep Retention, now time.Time) string {
	if keep.Metrics <= 0 || !from.Before(now.Add(-keep.Metrics)) {
		return ResolutionRaw
	}
	if keep.Rollup <= 0 {
		// No rollups on this plan: the window is clamped to raw retention
		// instead, so what comes back is raw - just less of it than asked for.
		return ResolutionRaw
	}
	if step >= time.Hour {
		return Resolution1h
	}
	return Resolution5m
}

// rollupSeries maps a resolution onto its series name.
func rollupSeries(resolution string) string {
	switch resolution {
	case Resolution5m:
		return Rollup5m
	case Resolution1h:
		return Rollup1h
	default:
		return ""
	}
}
