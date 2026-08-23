package alerting

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/klaro/observability/internal/plans"
	"github.com/klaro/observability/internal/tenants"
)

// Syncer keeps the vmalert rule file in step with the database.
//
// vmalert reads rules from a file, and the rules live in Postgres, so something
// has to bridge the two. It is a periodic full render rather than an
// incremental diff: the file is small, a full render is trivially correct, and
// an incremental one would have to reason about deletes, org removal and its own
// previous mistakes.
type Syncer struct {
	rules   *Store
	plans   *plans.Store
	tenants tenants.Mapper

	path      string
	reloadURL string
	opts      RenderOptions
	http      *http.Client
}

// SyncerOptions configures a Syncer.
type SyncerOptions struct {
	// Path is the rule file vmalert reads. Empty disables syncing entirely,
	// which is how a deployment without vmalert stays quiet instead of logging
	// a failure every interval.
	Path string
	// ReloadURL is vmalert's reload endpoint. Empty means rely on vmalert's own
	// file polling.
	ReloadURL string
	Render    RenderOptions
}

// NewSyncer wires a Syncer.
func NewSyncer(rules *Store, planStore *plans.Store, mapper tenants.Mapper, opts SyncerOptions) *Syncer {
	return &Syncer{
		rules: rules, plans: planStore, tenants: mapper,
		path: opts.Path, reloadURL: opts.ReloadURL, opts: opts.Render,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether this deployment syncs rules at all.
func (s *Syncer) Enabled() bool { return s != nil && s.path != "" }

// Sync renders every org's rules and writes the file.
func (s *Syncer) Sync(ctx context.Context) error {
	if !s.Enabled() {
		return nil
	}
	orgIDs, err := s.plans.Orgs(ctx)
	if err != nil {
		return err
	}

	groups := make([]OrgRules, 0, len(orgIDs))
	for _, orgID := range orgIDs {
		rules, err := s.rules.List(ctx, orgID)
		if err != nil {
			return fmt.Errorf("list rules for %s: %w", orgID, err)
		}
		plan, err := s.plans.Get(ctx, orgID)
		if err != nil {
			return fmt.Errorf("plan for %s: %w", orgID, err)
		}
		// Rollups are a plan feature: an org whose plan keeps no long-term
		// rollups should not pay the evaluation cost of producing them.
		downsample := plan.RollupRetention > 0
		if len(rules) == 0 && !downsample {
			continue
		}
		account, err := s.tenants.VMAccountID(ctx, orgID)
		if err != nil {
			return fmt.Errorf("tenant for %s: %w", orgID, err)
		}
		groups = append(groups, OrgRules{
			OrgID: orgID, VMAccountID: account, Rules: rules, Downsample: downsample,
		})
	}

	rendered, err := Render(groups, s.opts)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.path, rendered); err != nil {
		return err
	}
	return s.reload(ctx)
}

// writeAtomic replaces the file in one step.
//
// vmalert may be reading the file at any moment; a partial write would be a
// parse error that silently disables every rule until the next sync.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create rule dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".klaro-rules-*")
	if err != nil {
		return fmt.Errorf("create temp rule file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeds

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp rule file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp rule file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp rule file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace rule file: %w", err)
	}
	return nil
}

// reload asks vmalert to pick the file up now. A failure is logged, not
// returned: the file is already correct, and vmalert re-reads on its own
// schedule, so a missed reload delays rules rather than breaking them.
func (s *Syncer) reload(ctx context.Context) error {
	if s.reloadURL == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.reloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		log.Printf("vmalert reload failed (rules are on disk, vmalert will poll): %v", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		log.Printf("vmalert reload returned %s (rules are on disk)", resp.Status)
	}
	return nil
}

// Run syncs on a loop until the context ends.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		log.Print("alerting: rule sync disabled (no rule file configured)")
		return
	}
	if interval <= 0 {
		interval = time.Minute
	}
	// Sync once immediately: after a restart the file on disk may predate every
	// rule change made while this process was down.
	if err := s.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("alerting: initial rule sync failed: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Sync(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("alerting: rule sync failed: %v", err)
			}
		}
	}
}
