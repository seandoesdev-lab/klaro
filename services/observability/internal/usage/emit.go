package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/platform/redisx"
)

// EmitSubject is where billing usage is published (design section 4.5).
const EmitSubject = "klaro.usage.emitted"

// Meters. Two axes, per design HOW-6: active hosts is what the plan is priced
// on, ingest volume is the secondary guard that catches a small fleet shipping
// an enormous amount of data.
const (
	MeterHosts    = "observability_hosts"
	MeterIngestGB = "observability_ingest_gb"
)

// bytesPerGB is the decimal gigabyte: invoices are read in decimal units.
const bytesPerGB = 1_000_000_000.0

// Event is the published payload.
type Event struct {
	OrgID string `json:"org_id"`
	Meter string `json:"meter"`
	// Quantity is the increment since the last emission, which is what an
	// additive billing meter wants.
	Quantity float64 `json:"quantity"`
	Period   Period  `json:"period"`
	// Computation records how the number was reached. A customer disputing an
	// invoice deserves a better answer than "the meter said so".
	Computation map[string]any `json:"computation"`
	At          time.Time      `json:"at"`
}

// MarshalJSON renders Period as an object with start and end.
func (p Period) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]time.Time{"start": p.Start, "end": p.End})
}

// Emitter publishes usage increments.
type Emitter struct {
	store  *Store
	plans  *plans.Store
	signal redisx.Signaler

	activeHostWindow time.Duration
	now              func() time.Time
}

// NewEmitter wires an Emitter. activeHostWindow is how recently a host must
// have reported to count, matching the quota endpoint so a customer's invoice
// and their dashboard agree.
func NewEmitter(store *Store, planStore *plans.Store, signal redisx.Signaler,
	activeHostWindow time.Duration) *Emitter {
	if activeHostWindow <= 0 {
		activeHostWindow = 15 * time.Minute
	}
	return &Emitter{
		store: store, plans: planStore, signal: signal,
		activeHostWindow: activeHostWindow, now: time.Now,
	}
}

// currentSQL reads this month's meters for the caller org.
const currentSQL = `
WITH active AS (
  SELECT count(*)::int AS hosts FROM observability_hosts
   WHERE last_seen_at > now() - make_interval(secs => $1)
), volume AS (
  SELECT coalesce(sum(ingested_bytes), 0)::bigint AS bytes
    FROM observability_usage_rollups
   WHERE period_start = date_trunc('month', now())
)
SELECT active.hosts, volume.bytes,
       date_trunc('month', now()),
       date_trunc('month', now()) + interval '1 month'
FROM active, volume`

// advanceHostMaxSQL keeps the month's high-water mark on the rollup row, so the
// number the invoice was built from sits next to the volume it came with.
const advanceHostMaxSQL = `
INSERT INTO observability_usage_rollups
  (org_id, period_start, period_end, signal, ingested_bytes, host_count_max)
VALUES (current_setting('app.current_org')::uuid,
        date_trunc('month', now()),
        date_trunc('month', now()) + interval '1 month',
        'metrics', 0, $1)
ON CONFLICT (org_id, period_start, signal) DO UPDATE
SET host_count_max = greatest(coalesce(observability_usage_rollups.host_count_max, 0), EXCLUDED.host_count_max)`

// RunOnce meters every org and publishes what changed.
func (e *Emitter) RunOnce(ctx context.Context) (int, error) {
	orgIDs, err := e.plans.Orgs(ctx)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, orgID := range orgIDs {
		if ctx.Err() != nil {
			return published, ctx.Err()
		}
		n, err := e.emitOrg(ctx, orgID)
		if err != nil {
			log.Printf("usage: emit for %s: %v", orgID, err)
			continue
		}
		published += n
	}
	return published, nil
}

func (e *Emitter) emitOrg(ctx context.Context, orgID string) (int, error) {
	var (
		hosts  int
		volume int64
		period Period
	)
	err := e.store.DB().WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, currentSQL, e.activeHostWindow.Seconds()).
			Scan(&hosts, &volume, &period.Start, &period.End); err != nil {
			return fmt.Errorf("read meters: %w", err)
		}
		if hosts > 0 {
			if _, err := tx.Exec(ctx, advanceHostMaxSQL, hosts); err != nil {
				return fmt.Errorf("advance host max: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	plan, planErr := e.plans.Get(ctx, orgID)
	published := 0

	// Hosts: the increment of the high-water mark. Summed by the billing side,
	// the increments reconstruct the month's peak.
	if hosts > 0 {
		delta, err := e.claim(ctx, orgID, MeterHosts, period, float64(hosts), map[string]any{
			"basis":              "max active hosts in period",
			"active_host_window": e.activeHostWindow.String(),
			"plan":               planCode(plan, planErr),
			"host_limit":         plan.MaxHosts,
		})
		if err != nil {
			return published, err
		}
		if delta > 0 {
			published++
		}
	}

	// Ingest volume: a cumulative total in decimal GB.
	gb := float64(volume) / bytesPerGB
	if gb > 0 {
		delta, err := e.claim(ctx, orgID, MeterIngestGB, period, gb, map[string]any{
			"basis":            "sum of ingested_bytes across signals in period",
			"cumulative_bytes": volume,
			"plan":             planCode(plan, planErr),
			"ingest_limit_gb":  plan.MaxIngestGBMon,
		})
		if err != nil {
			return published, err
		}
		if delta > 0 {
			published++
		}
	}
	return published, nil
}

func planCode(p plans.Plan, err error) string {
	if err != nil {
		return ""
	}
	return p.Code
}

// readLedgerSQL locks this period's ledger row so two passes cannot both see
// the old cumulative value and both publish the same increment.
const readLedgerSQL = `
SELECT quantity FROM observability_usage_emissions
 WHERE period_start = $1 AND meter = $2
 FOR UPDATE`

// writeLedgerSQL advances the cumulative value.
const writeLedgerSQL = `
INSERT INTO observability_usage_emissions
  (org_id, period_start, period_end, meter, quantity, computation)
VALUES (current_setting('app.current_org')::uuid, $1, $2, $3, $4, $5)
ON CONFLICT (org_id, period_start, meter) DO UPDATE
SET quantity = EXCLUDED.quantity, computation = EXCLUDED.computation, emitted_at = now()`

// claim advances the ledger and publishes the increment.
//
// The ledger row is the idempotency key: a restart, an overlapping cron or a
// duplicated pass all read the same previous value and publish the same delta,
// or nothing when the delta is zero. Billing is one of the few places where
// doing the work twice is worse than not doing it at all.
func (e *Emitter) claim(ctx context.Context, orgID, meter string, period Period,
	cumulative float64, computation map[string]any) (float64, error) {
	raw, err := json.Marshal(computation)
	if err != nil {
		return 0, err
	}

	var previous float64
	advanced := false
	err = e.store.DB().WithOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, readLedgerSQL, period.Start, meter).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read emission ledger: %w", err)
		}
		if cumulative <= previous {
			return nil
		}
		if _, err := tx.Exec(ctx, writeLedgerSQL,
			period.Start, period.End, meter, cumulative, raw); err != nil {
			return fmt.Errorf("record emission: %w", err)
		}
		advanced = true
		return nil
	})
	if err != nil {
		return 0, err
	}
	if !advanced {
		return 0, nil
	}

	computation["cumulative_quantity"] = cumulative
	computation["previously_emitted"] = previous
	event := Event{
		OrgID: orgID, Meter: meter, Quantity: cumulative - previous, Period: period,
		Computation: computation, At: e.now(),
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	if err := e.signal.Publish(ctx, EmitSubject, payload); err != nil {
		// The ledger already advanced, so this increment will not be re-sent.
		// That is the right trade: double billing is worse than a gap, and the
		// ledger keeps the evidence of what was measured either way.
		log.Printf("usage: publish %s for %s failed after the ledger advanced: %v", meter, orgID, err)
	}
	return event.Quantity, nil
}

// Run meters on a loop.
func (e *Emitter) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := e.RunOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("usage: pass failed: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("usage: published %d meter increments", n)
			}
		}
	}
}
