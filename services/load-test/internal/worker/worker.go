package worker

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"time"

	"github.com/klaro/load-test/internal/model"
	"github.com/klaro/load-test/internal/queue"
	"github.com/klaro/load-test/internal/scenario"
	"github.com/klaro/load-test/internal/store"
)

type Deps struct {
	Queue  queue.JobQueue
	Signal queue.Signaler
	Store  *store.Store
	K6Path string
}

// Run consumes jobs until ctx is cancelled.
func Run(ctx context.Context, d Deps) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		job, err := d.Queue.Dequeue(ctx)
		if err != nil {
			continue // timeout or transient; loop again
		}
		if err := d.process(ctx, job); err != nil {
			log.Printf("job %s failed: %v", job.LoadTestID, err)
		}
	}
}

func (d Deps) process(ctx context.Context, job model.Job) error {
	id := job.LoadTestID
	_ = d.Store.UpdateStatus(ctx, id, model.StatusProvisioning, nil)
	script, err := scenario.Generate(job.Scenario)
	if err != nil {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusFailed, nil)
		return err
	}
	_ = d.Store.UpdateStatus(ctx, id, model.StatusRunning, nil)
	_ = d.Store.MarkStarted(ctx, id)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// abort signal (user-triggered)
	abortCh, abortCancel := d.Signal.SubscribeAbort(ctx, id)
	defer abortCancel()
	var abortReason string
	go func() {
		select {
		case <-abortCh:
			abortReason = "aborted by user"
			cancel()
		case <-runCtx.Done():
		}
	}()

	agg := NewAggregator()
	brk := newBreaker()
	win := newWindow()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case now := <-ticker.C:
				snap := win.flush()
				payload, _ := json.Marshal(snap.toWSMessage(now, job.Scenario.VU))
				_ = d.Signal.PublishMetric(ctx, id, payload)
				if trip, reason := brk.ShouldAbort(now); trip {
					abortReason = reason
					cancel()
				}
			}
		}
	}()

	runErr := runK6(runCtx, d.K6Path, script, job.TargetURL, func(p metricPoint) {
		agg.Add(p)
		if p.metric == "http_req_duration" {
			win.add(p)
			brk.Observe(p.ts, !isError(p.status))
		}
	})
	_ = runErr // k6 exits non-zero on threshold breach; we still aggregate below

	if abortReason != "" {
		reason := abortReason
		_ = d.Store.UpdateStatus(ctx, id, model.StatusAborted, &reason)
		payload, _ := json.Marshal(map[string]any{"event": "aborted", "reason": reason})
		_ = d.Signal.PublishMetric(ctx, id, payload)
	} else {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusAggregating, nil)
	}

	summary := agg.Summary(job.Scenario.VU)
	if err := d.Store.SaveResult(ctx, id, summary); err != nil {
		return err
	}
	_ = d.Store.MarkFinished(ctx, id)
	if abortReason == "" {
		_ = d.Store.UpdateStatus(ctx, id, model.StatusCompleted, nil)
	}
	return nil
}

// --- per-window accumulation for the live stream ---

type window struct {
	durations []float64
	total     int
	errors    int
}

func newWindow() *window { return &window{} }

func (w *window) add(p metricPoint) {
	w.durations = append(w.durations, p.value)
	w.total++
	if isError(p.status) {
		w.errors++
	}
}

type windowSnap struct {
	count   int
	errRate float64
	p95     float64
}

func (w *window) flush() windowSnap {
	snap := windowSnap{count: w.total}
	if w.total > 0 {
		snap.errRate = float64(w.errors) / float64(w.total)
		sorted := append([]float64(nil), w.durations...)
		sort.Float64s(sorted)
		snap.p95 = percentile(sorted, 0.95)
	}
	w.durations = nil
	w.total = 0
	w.errors = 0
	return snap
}

func (s windowSnap) toWSMessage(now time.Time, vu int) map[string]any {
	return map[string]any{
		"ts":             now.UTC().Format(time.RFC3339),
		"rps":            s.count,
		"latency_p95_ms": s.p95,
		"error_rate":     s.errRate,
		"active_vu":      vu,
	}
}
