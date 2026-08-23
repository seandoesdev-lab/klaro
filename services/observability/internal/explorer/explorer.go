// Package explorer is the read path for stored telemetry (OBS-03/04/05).
//
// It is a thin proxy, not a passthrough. Every query is assembled here from
// structured parameters, and the org is injected server side - as the
// VictoriaMetrics tenant in the URL, as the X-Scope-OrgID header for Tempo and
// Loki, and as a label matcher on top of both. A caller never supplies raw
// MetricsQL, TraceQL or LogQL, because a raw query is a query whose label
// matchers the caller can take off (design HOW-2).
//
// The cost of that is expressiveness: this covers the structured cases the
// dashboard needs, with a whitelist of aggregations. Opening up raw queries
// later means putting a label-enforcing proxy in front, not relaxing this.
package explorer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/klaro/observability/internal/tenants"
)

// Errors callers distinguish.
var (
	// ErrInvalidQuery is a caller mistake: bad label, unknown aggregation,
	// unusable time range.
	ErrInvalidQuery = errors.New("invalid explorer query")
	// ErrBackend is the storage backend failing or refusing.
	ErrBackend = errors.New("telemetry backend error")
	// ErrNotFound is a trace (or other addressable object) that does not exist
	// inside the caller org.
	ErrNotFound = errors.New("not found")
)

// scopeHeader is the tenant header Tempo and Loki enforce on.
const scopeHeader = "X-Scope-OrgID"

// Config locates the storage backends.
type Config struct {
	// VMSelectURL is the vmselect base, e.g. http://vmselect:8481. The tenant
	// is a path segment, so it is appended per request.
	VMSelectURL string
	// TempoURL is the Tempo query frontend, e.g. http://tempo:3200.
	TempoURL string
	// LokiURL is the Loki read path, e.g. http://loki:3100.
	LokiURL string

	// Timeout bounds one backend request.
	Timeout time.Duration
	// MaxLimit caps the rows any single query may ask for, so one request
	// cannot pull an entire retention window into memory.
	MaxLimit int
}

// Defaults for anything left unset.
const (
	DefaultTimeout  = 30 * time.Second
	DefaultMaxLimit = 1000
)

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxLimit <= 0 {
		c.MaxLimit = DefaultMaxLimit
	}
	return c
}

// Configured reports whether a backend was pointed at anything. Handlers use it
// to answer "this deployment has no Tempo" instead of dialling an empty URL.
func (c Config) Configured(base string) bool { return strings.TrimSpace(base) != "" }

// Client queries the storage backends on behalf of one org at a time.
type Client struct {
	cfg     Config
	tenants tenants.Mapper
	http    *http.Client
}

// New builds a Client. mapper is what turns an org into the tenant identity of
// each backend; without it there is no isolation, so it is required.
func New(cfg Config, mapper tenants.Mapper) *Client {
	cfg = cfg.withDefaults()
	return &Client{
		cfg:     cfg,
		tenants: mapper,
		http:    &http.Client{Timeout: cfg.Timeout},
	}
}

// TimeRange is the window a query covers.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Validate rejects a range that cannot produce a sensible answer.
func (r TimeRange) Validate() error {
	if r.From.IsZero() || r.To.IsZero() {
		return fmt.Errorf("%w: from and to are required", ErrInvalidQuery)
	}
	if !r.To.After(r.From) {
		return fmt.Errorf("%w: to must be after from", ErrInvalidQuery)
	}
	return nil
}

// getJSON performs one backend GET and decodes the body into out.
//
// The backend response is never handed to the caller verbatim: an upstream body
// can carry cluster topology, other tenants' label names in an error string, or
// a query it echoes back. Everything reaching the client goes through a mapper
// in this package.
func (c *Client) getJSON(ctx context.Context, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrBackend, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBackend, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode >= 300:
		// Read a little of the body for the server log, but never return it.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%w: %s: %s", ErrBackend, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%w: decode response: %v", ErrBackend, err)
	}
	return nil
}

// scopeFor resolves the Tempo/Loki tenant header value for an org.
func (c *Client) scopeFor(ctx context.Context, orgID string) (map[string]string, error) {
	scope, err := c.tenants.ScopeOrgID(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve tenant: %v", ErrBackend, err)
	}
	return map[string]string{scopeHeader: scope}, nil
}

// join builds a backend URL, keeping exactly one slash at the seam.
func join(base, path string, query url.Values) string {
	u := strings.TrimSuffix(base, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}
