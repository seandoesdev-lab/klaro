// Package retention enforces per-plan data lifetime (OBS-08, design HOW-5).
//
// Enforcement is layered, because the three backends can do very different
// things and only one of them can delete a time range:
//
//   - The Explorer narrows every query to the plan window. That is what makes
//     "data past the plan is not queryable" exact and immediate, and it is the
//     part the contract actually asks for.
//   - Loki has a real time-ranged delete API, so old log lines are physically
//     removed per org.
//   - VictoriaMetrics can only delete whole series by matcher, never a time
//     range, so this job removes the series of orgs that have stopped sending
//     entirely. Raw samples of a still-active series age out through the
//     cluster-wide retention, set to the longest plan.
//   - Tempo expires whole blocks on a global schedule and exposes no per-tenant
//     delete. There is nothing to call, so this job says so once rather than
//     pretending.
//
// Every gap above is a real limitation of the OSS backends, not an unfinished
// intention. They are logged, and the Explorer clamp keeps the customer promise
// regardless.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/tenants"
)

// Config locates the backends this job can act on.
type Config struct {
	// VMSelectURL hosts both the series API and the delete API in cluster mode.
	VMSelectURL string
	// LokiURL is the Loki base; deletes go to its compactor-backed delete API.
	LokiURL string
	// TempoURL is recorded only so the log line can name what was skipped.
	TempoURL string
	// Timeout bounds one backend call.
	Timeout time.Duration
	// DryRun logs what would be deleted without deleting it. Worth having: the
	// first run of a data-deleting job in a new environment should be readable
	// before it is trusted.
	DryRun bool
}

// Job enforces retention across orgs.
type Job struct {
	cfg     Config
	plans   *plans.Store
	tenants tenants.Mapper
	http    *http.Client
	now     func() time.Time

	tempoWarned bool
}

// New builds a Job.
func New(cfg Config, planStore *plans.Store, mapper tenants.Mapper) *Job {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &Job{
		cfg: cfg, plans: planStore, tenants: mapper,
		http: &http.Client{Timeout: cfg.Timeout},
		now:  time.Now,
	}
}

// Report is what one pass did, for logs and tests.
type Report struct {
	Orgs            int
	MetricsPruned   int
	LogDeletesFiled int
	Skipped         int
}

// RunOnce enforces retention for every org.
func (j *Job) RunOnce(ctx context.Context) (Report, error) {
	var rep Report
	orgIDs, err := j.plans.Orgs(ctx)
	if err != nil {
		return rep, err
	}

	for _, orgID := range orgIDs {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		plan, err := j.plans.Get(ctx, orgID)
		if err != nil {
			log.Printf("retention: plan for %s: %v", orgID, err)
			rep.Skipped++
			continue
		}
		rep.Orgs++

		if j.pruneMetrics(ctx, orgID, plan) {
			rep.MetricsPruned++
		}
		if j.deleteLogs(ctx, orgID, plan) {
			rep.LogDeletesFiled++
		}
		j.noteTraces(plan)
	}
	return rep, nil
}

// pruneMetrics drops an org's series once nothing is left inside its window.
//
// The check is two queries, not one: "has data before the cutoff" and "has no
// data after it". Acting on the second alone would erase an org that simply has
// not sent anything yet, and delete_series takes no time range, so there would
// be no way to get it back.
func (j *Job) pruneMetrics(ctx context.Context, orgID string, plan plans.Plan) bool {
	if j.cfg.VMSelectURL == "" || plan.MetricsRetention <= 0 {
		return false
	}
	account, err := j.tenants.VMAccountID(ctx, orgID)
	if err != nil {
		log.Printf("retention: tenant for %s: %v", orgID, err)
		return false
	}
	tenant := tenants.VMTenantPath(account)
	// Rollups outlive raw data, so the cutoff for removing an org outright is
	// the longer of the two windows.
	keep := plan.MetricsRetention
	if plan.RollupRetention > keep {
		keep = plan.RollupRetention
	}
	cutoff := j.now().Add(-keep)
	matcher := fmt.Sprintf("{%s=%s}", explorer.OrgLabel, strconv.Quote(orgID))

	recent, err := j.seriesCount(ctx, tenant, matcher, cutoff, j.now())
	if err != nil {
		log.Printf("retention: probe recent series for %s: %v", orgID, err)
		return false
	}
	if recent > 0 {
		return false // still active: old samples age out with cluster retention
	}
	old, err := j.seriesCount(ctx, tenant, matcher, time.Unix(0, 0), cutoff)
	if err != nil {
		log.Printf("retention: probe old series for %s: %v", orgID, err)
		return false
	}
	if old == 0 {
		return false // nothing to do
	}

	if j.cfg.DryRun {
		log.Printf("retention: would remove %d expired metric series for org %s (tenant %s)", old, orgID, tenant)
		return false
	}
	if err := j.dropSeries(ctx, tenant, matcher); err != nil {
		log.Printf("retention: remove series for %s: %v", orgID, err)
		return false
	}
	log.Printf("retention: removed %d expired metric series for org %s", old, orgID)
	return true
}

// seriesCount asks how many series match inside a window.
func (j *Job) seriesCount(ctx context.Context, tenant, matcher string, from, to time.Time) (int, error) {
	params := url.Values{}
	params.Set("match[]", matcher)
	params.Set("start", strconv.FormatInt(from.Unix(), 10))
	params.Set("end", strconv.FormatInt(to.Unix(), 10))

	endpoint := strings.TrimSuffix(j.cfg.VMSelectURL, "/") +
		"/select/" + tenant + "/prometheus/api/v1/series?" + params.Encode()

	var body struct {
		Status string           `json:"status"`
		Data   []map[string]any `json:"data"`
	}
	if err := j.getJSON(ctx, endpoint, nil, &body); err != nil {
		return 0, err
	}
	return len(body.Data), nil
}

// dropSeries removes every series matching the org.
func (j *Job) dropSeries(ctx context.Context, tenant, matcher string) error {
	params := url.Values{}
	params.Set("match[]", matcher)
	endpoint := strings.TrimSuffix(j.cfg.VMSelectURL, "/") +
		"/delete/" + tenant + "/prometheus/api/v1/admin/tsdb/delete_series?" + params.Encode()
	return j.post(ctx, endpoint, nil)
}

// deleteLogs files a Loki delete request for everything older than the window.
//
// Loki's delete is asynchronous - the compactor applies it later - so a filed
// request is the most this job can confirm. The Explorer clamp is what makes the
// data unreadable in the meantime.
func (j *Job) deleteLogs(ctx context.Context, orgID string, plan plans.Plan) bool {
	if j.cfg.LokiURL == "" || plan.LogsRetention <= 0 {
		return false
	}
	scope, err := j.tenants.ScopeOrgID(ctx, orgID)
	if err != nil {
		log.Printf("retention: scope for %s: %v", orgID, err)
		return false
	}
	cutoff := j.now().Add(-plan.LogsRetention)

	params := url.Values{}
	params.Set("query", fmt.Sprintf("{%s=%s}", explorer.OrgLabel, strconv.Quote(orgID)))
	params.Set("start", "0")
	params.Set("end", strconv.FormatInt(cutoff.Unix(), 10))
	endpoint := strings.TrimSuffix(j.cfg.LokiURL, "/") + "/loki/api/v1/delete?" + params.Encode()

	if j.cfg.DryRun {
		log.Printf("retention: would file Loki delete for org %s up to %s",
			orgID, cutoff.UTC().Format(time.RFC3339))
		return false
	}
	if err := j.post(ctx, endpoint, map[string]string{"X-Scope-OrgID": scope}); err != nil {
		log.Printf("retention: loki delete for %s: %v", orgID, err)
		return false
	}
	return true
}

// noteTraces says once per process what cannot be enforced per org.
func (j *Job) noteTraces(plan plans.Plan) {
	if j.tempoWarned || j.cfg.TempoURL == "" || plan.TracesRetention <= 0 {
		return
	}
	j.tempoWarned = true
	log.Print("retention: Tempo exposes no per-tenant delete; trace retention relies on " +
		"the cluster-wide block_retention plus the Explorer window clamp")
}

// Run enforces retention on a loop.
func (j *Job) Run(ctx context.Context, interval time.Duration) {
	if j.cfg.VMSelectURL == "" && j.cfg.LokiURL == "" {
		log.Print("retention: no backends configured, job disabled")
		return
	}
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rep, err := j.RunOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("retention: pass failed: %v", err)
				continue
			}
			log.Printf("retention: pass complete orgs=%d metrics_pruned=%d log_deletes=%d skipped=%d",
				rep.Orgs, rep.MetricsPruned, rep.LogDeletesFiled, rep.Skipped)
		}
	}
}
