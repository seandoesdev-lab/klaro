// Package inventory is the host inventory behind GET /obs/hosts (P1b).
//
// It joins two stores that are deliberately not the same store:
//
//   - observability_hosts in Postgres is the registry. It is written by the
//     usage path when the gateway reports a host, is scoped by RLS, and is what
//     the host quota is counted from (design HOW-4/HOW-6).
//   - VictoriaMetrics holds the readings. Time series stay out of the relational
//     database (CLAUDE.md), so cpu/mem/disk/load are read back through the
//     Explorer, which pins the org's VM tenant and injects the org matcher.
//
// The join key is host_ident, which is the same string the remote write
// exporter puts in the `instance` label. Doing the join here rather than in SQL
// is what keeps that invariant intact: there is no table to be tempted to
// denormalise a metric into.
//
// A host known to the registry but silent in VictoriaMetrics keeps its row with
// null readings. That case is the point of having a registry at all - it is how
// a host that stopped reporting stays visible instead of disappearing from the
// screen exactly when someone needs to notice it.
package inventory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/explorer"
	"github.com/klaro/observability/internal/platform/db"
)

// Status values a host row can carry.
const (
	// StatusUp means the host reported inside the active window.
	StatusUp = "up"
	// StatusStale means it is registered but has not reported recently. It is
	// not "down": this platform observes reporting, not reachability, and
	// calling a host down because its agent stopped would be a claim about
	// something we did not measure.
	StatusStale = "stale"
)

// Registration is one row of observability_hosts.
type Registration struct {
	HostIdent   string    `json:"host_ident"`
	Service     string    `json:"service,omitempty"`
	Env         string    `json:"env,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// Host is a registration with its latest readings attached.
type Host struct {
	Registration
	explorer.HostVitals
	Status string `json:"status"`
}

// Result is the GET /obs/hosts response body.
type Result struct {
	Data []Host `json:"data"`
	// ActiveWindowSec is the threshold that separates up from stale, echoed so
	// the client labels the column with the rule the server actually applied
	// rather than one it assumed.
	ActiveWindowSec int `json:"active_window_sec"`
	// Total is every registered host; Active is how many are inside the window
	// (the same count the quota meters).
	Total  int `json:"total"`
	Active int `json:"active"`
	// MetricsAvailable is false when the readings could not be fetched. The
	// registry half still answers, so the screen shows the fleet with empty
	// gauges and says why, instead of failing whole.
	MetricsAvailable bool `json:"metrics_available"`
}

// Metrics is the reading half of the inventory. An interface so this package
// depends on the capability rather than on the Explorer client, which keeps the
// join testable without a VictoriaMetrics stub.
type Metrics interface {
	Vitals(ctx context.Context, orgID string, lookback time.Duration) (map[string]explorer.HostVitals, error)
}

// Service answers host inventory queries.
type Service struct {
	db      *db.DB
	metrics Metrics
	// activeWindow is how recently a host must have reported to count as up. It
	// is the same value the quota meters against, passed in rather than
	// redeclared so the inventory and the invoice cannot disagree about which
	// hosts are active.
	activeWindow time.Duration
}

// DefaultActiveWindow is used when the caller configures none.
const DefaultActiveWindow = 15 * time.Minute

// New builds a Service. metrics may be nil, which yields registry-only results.
func New(d *db.DB, metrics Metrics, activeWindow time.Duration) *Service {
	if activeWindow <= 0 {
		activeWindow = DefaultActiveWindow
	}
	return &Service{db: d, metrics: metrics, activeWindow: activeWindow}
}

// listSQL reads the registry for the caller org.
//
// No org predicate: the transaction has already set app.current_org and the RLS
// policy on observability_hosts (migration 0002) is the filter. Writing one
// here as well would create a second place for the scoping rule to live, and
// the day they disagree the SQL wins silently.
const listSQL = `
SELECT host_ident, coalesce(service, ''), coalesce(env, ''), first_seen_at, last_seen_at
FROM observability_hosts
ORDER BY last_seen_at DESC, host_ident
LIMIT $1`

// MaxHosts bounds one inventory response. A fleet larger than this needs
// pagination and a server-side sort, which is a later decision - answering with
// a truncated list and no signal would be worse than the limit.
const MaxHosts = 2000

// List returns the org's hosts with their latest readings.
func (s *Service) List(ctx context.Context, orgID string) (Result, error) {
	out := Result{Data: []Host{}, ActiveWindowSec: int(s.activeWindow.Seconds())}
	if s.db == nil {
		return Result{}, fmt.Errorf("inventory: no database configured")
	}

	var rows []Registration
	err := s.db.WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		r, err := tx.Query(ctx, listSQL, MaxHosts)
		if err != nil {
			return fmt.Errorf("list observability hosts: %w", err)
		}
		defer r.Close()
		for r.Next() {
			var reg Registration
			if err := r.Scan(&reg.HostIdent, &reg.Service, &reg.Env,
				&reg.FirstSeenAt, &reg.LastSeenAt); err != nil {
				return fmt.Errorf("scan observability host: %w", err)
			}
			rows = append(rows, reg)
		}
		return r.Err()
	})
	if err != nil {
		return Result{}, err
	}

	// The readings are fetched once for the whole fleet rather than per host:
	// the vitals queries already aggregate by instance, so one round trip per
	// metric answers for every host at once.
	vitals := map[string]explorer.HostVitals{}
	if s.metrics != nil {
		v, err := s.metrics.Vitals(ctx, orgID, explorer.DefaultHostLookback)
		if err == nil {
			vitals, out.MetricsAvailable = v, true
		}
		// A backend failure is deliberately swallowed: MetricsAvailable already
		// carries the fact, and the registry answer is the more important half.
	}

	cutoff := time.Now().Add(-s.activeWindow)
	for _, reg := range rows {
		h := Host{Registration: reg, HostVitals: vitals[reg.HostIdent], Status: StatusStale}
		if reg.LastSeenAt.After(cutoff) {
			h.Status = StatusUp
			out.Active++
		}
		out.Data = append(out.Data, h)
	}
	out.Total = len(out.Data)

	// Busiest first, so the hostmap and the table agree on what matters and a
	// hot host is on screen without scrolling. Hosts with no reading sort last
	// rather than as zero - unknown is not idle.
	sort.SliceStable(out.Data, func(i, j int) bool {
		return busyness(out.Data[i]) > busyness(out.Data[j])
	})
	return out, nil
}

// busyness ranks a host for display: the higher of its cpu and memory
// utilisation, or -1 when neither was read.
func busyness(h Host) float64 {
	worst := -1.0
	if h.CPUPct != nil && *h.CPUPct > worst {
		worst = *h.CPUPct
	}
	if h.MemPct != nil && *h.MemPct > worst {
		worst = *h.MemPct
	}
	return worst
}
