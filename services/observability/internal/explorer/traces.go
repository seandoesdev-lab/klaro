package explorer

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TracesQuery is the structured form of GET /obs/traces [OBS-04].
type TracesQuery struct {
	Range       TimeRange
	Service     string
	MinDuration time.Duration
	Limit       int
}

// TraceSummary is one search hit.
type TraceSummary struct {
	TraceID     string  `json:"trace_id"`
	RootService string  `json:"root_service"`
	RootName    string  `json:"root_name,omitempty"`
	DurationMS  float64 `json:"duration_ms"`
	Start       int64   `json:"start"`
}

// TracesResult is the search response.
type TracesResult struct {
	Data  []TraceSummary `json:"data"`
	Query string         `json:"query"`
}

// Span is one node of the waterfall.
type Span struct {
	SpanID       string `json:"span_id"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	Name         string `json:"name"`
	Service      string `json:"service"`
	// Start is unix milliseconds. A waterfall cannot be drawn from durations
	// alone - the offsets are the whole picture - so it is returned even though
	// the design's field list only names duration.
	Start      int64   `json:"start"`
	DurationMS float64 `json:"duration_ms"`
	Status     string  `json:"status"`
	// Host is the resource's service.instance.id - the host_ident the SDK
	// contract normalises (sdk/SDK_CONTRACT.md section 3).
	//
	// It is the secondary correlation key. trace_id joins a span to its log
	// lines, but a metric series carries no trace id at all - a counter is not
	// per-request - so a span reaches its metrics through service + host and
	// nothing else. Without it a span's metrics tab would have to guess which
	// instance it was looking at.
	Host string `json:"host,omitempty"`
}

// Trace is the waterfall response.
type Trace struct {
	TraceID string `json:"trace_id"`
	Spans   []Span `json:"spans"`
}

// maxServiceName bounds the service filter. Service names are short; a long one
// is someone trying to write TraceQL by hand.
const maxServiceName = 256

// Validate checks the search parameters and applies the limit ceiling.
func (q *TracesQuery) Validate(maxLimit int) error {
	if err := q.Range.Validate(); err != nil {
		return err
	}
	if q.Service != "" && !validServiceName(q.Service) {
		return fmt.Errorf("%w: service name is too long or contains control characters", ErrInvalidQuery)
	}
	if q.MinDuration < 0 {
		return fmt.Errorf("%w: min_duration_ms must not be negative", ErrInvalidQuery)
	}
	if q.Limit <= 0 || q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return nil
}

func validServiceName(s string) bool {
	if len(s) > maxServiceName {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// traceQL renders the search expression.
//
// The caller never supplies TraceQL; it supplies a service name and a duration,
// and this builds the expression around them. The service name is emitted with
// strconv.Quote, so a value containing a quote cannot close the literal and
// open a new condition.
//
// Tenant isolation here is the X-Scope-OrgID header, not a condition in the
// expression: Tempo keys its blocks by tenant, so a query cannot reach another
// org's blocks whatever the expression says.
func traceQL(service string, minDuration time.Duration) string {
	var conds []string
	if service != "" {
		conds = append(conds, "resource.service.name = "+strconv.Quote(service))
	}
	if minDuration > 0 {
		conds = append(conds, "duration >= "+strconv.FormatInt(minDuration.Milliseconds(), 10)+"ms")
	}
	if len(conds) == 0 {
		// Tempo needs an expression; this one matches every span in the window,
		// which is what "no filters" means.
		return "{}"
	}
	return "{" + strings.Join(conds, " && ") + "}"
}

// tempoSearchResponse is the subset of Tempo's search result this package maps.
type tempoSearchResponse struct {
	Traces []struct {
		TraceID           string `json:"traceID"`
		RootServiceName   string `json:"rootServiceName"`
		RootTraceName     string `json:"rootTraceName"`
		StartTimeUnixNano string `json:"startTimeUnixNano"`
		DurationMs        int64  `json:"durationMs"`
	} `json:"traces"`
}

// SearchTraces finds traces in the org's Tempo tenant [OBS-04].
func (c *Client) SearchTraces(ctx context.Context, orgID string, q TracesQuery) (TracesResult, error) {
	if !c.cfg.Configured(c.cfg.TempoURL) {
		return TracesResult{}, fmt.Errorf("%w: no traces backend configured", ErrBackend)
	}
	if err := q.Validate(c.cfg.MaxLimit); err != nil {
		return TracesResult{}, err
	}
	headers, err := c.scopeFor(ctx, orgID)
	if err != nil {
		return TracesResult{}, err
	}
	// Traces have no rollup - there is no standard downsampling for them - so
	// retention is enforced by narrowing the window and nothing else.
	q.Range, _ = clamp(q.Range, c.retentionFor(ctx, orgID).Traces, time.Now())

	expr := traceQL(q.Service, q.MinDuration)
	params := url.Values{}
	params.Set("q", expr)
	params.Set("start", strconv.FormatInt(q.Range.From.Unix(), 10))
	params.Set("end", strconv.FormatInt(q.Range.To.Unix(), 10))
	params.Set("limit", strconv.Itoa(q.Limit))

	var raw tempoSearchResponse
	if err := c.getJSON(ctx, join(c.cfg.TempoURL, "/api/search", params), headers, &raw); err != nil {
		return TracesResult{}, err
	}

	out := TracesResult{Data: make([]TraceSummary, 0, len(raw.Traces)), Query: expr}
	for _, t := range raw.Traces {
		startNano, _ := strconv.ParseInt(t.StartTimeUnixNano, 10, 64)
		out.Data = append(out.Data, TraceSummary{
			TraceID:     t.TraceID,
			RootService: t.RootServiceName,
			RootName:    t.RootTraceName,
			DurationMS:  float64(t.DurationMs),
			Start:       startNano / int64(time.Millisecond),
		})
	}
	return out, nil
}

// ValidTraceID reports whether s is a hex trace id Tempo would accept. Checking
// here keeps an arbitrary string out of the upstream URL path.
func ValidTraceID(s string) bool {
	if len(s) < 16 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') && !(ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}

// tempoTraceResponse is the OTLP-shaped body Tempo returns for one trace.
type tempoTraceResponse struct {
	Batches []struct {
		Resource struct {
			Attributes []struct {
				Key   string `json:"key"`
				Value struct {
					StringValue *string `json:"stringValue"`
				} `json:"value"`
			} `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []struct {
				SpanID            string `json:"spanId"`
				ParentSpanID      string `json:"parentSpanId"`
				Name              string `json:"name"`
				StartTimeUnixNano string `json:"startTimeUnixNano"`
				EndTimeUnixNano   string `json:"endTimeUnixNano"`
				Status            struct {
					Code string `json:"code"`
				} `json:"status"`
			} `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"batches"`
}

// GetTrace returns one trace as a waterfall [OBS-04].
//
// A trace id from another org simply is not in this tenant's blocks, so the
// tenant header turns a cross-tenant read into a 404 rather than a leak.
func (c *Client) GetTrace(ctx context.Context, orgID, traceID string) (Trace, error) {
	if !c.cfg.Configured(c.cfg.TempoURL) {
		return Trace{}, fmt.Errorf("%w: no traces backend configured", ErrBackend)
	}
	if !ValidTraceID(traceID) {
		return Trace{}, fmt.Errorf("%w: trace id must be 16-32 hex characters", ErrInvalidQuery)
	}
	headers, err := c.scopeFor(ctx, orgID)
	if err != nil {
		return Trace{}, err
	}

	var raw tempoTraceResponse
	if err := c.getJSON(ctx, join(c.cfg.TempoURL, "/api/traces/"+traceID, nil), headers, &raw); err != nil {
		return Trace{}, err
	}

	out := Trace{TraceID: traceID, Spans: []Span{}}
	for _, batch := range raw.Batches {
		service, host := "", ""
		for _, a := range batch.Resource.Attributes {
			if a.Value.StringValue == nil {
				continue
			}
			switch a.Key {
			case "service.name":
				service = *a.Value.StringValue
			case "service.instance.id":
				host = *a.Value.StringValue
			}
		}
		for _, ss := range batch.ScopeSpans {
			for _, s := range ss.Spans {
				start, _ := strconv.ParseInt(s.StartTimeUnixNano, 10, 64)
				end, _ := strconv.ParseInt(s.EndTimeUnixNano, 10, 64)
				out.Spans = append(out.Spans, Span{
					SpanID:       normalizeID(s.SpanID),
					ParentSpanID: normalizeID(s.ParentSpanID),
					Name:         s.Name,
					Service:      service,
					Host:         host,
					Start:        start / int64(time.Millisecond),
					DurationMS:   float64(end-start) / float64(time.Millisecond),
					Status:       spanStatus(s.Status.Code),
				})
			}
		}
	}
	if len(out.Spans) == 0 {
		return Trace{}, ErrNotFound
	}
	// A waterfall is read top to bottom in start order; sorting here means every
	// client does not have to.
	sort.SliceStable(out.Spans, func(i, j int) bool { return out.Spans[i].Start < out.Spans[j].Start })
	return out, nil
}

// spanStatus normalises the OTLP status code, which Tempo emits as a name and
// sometimes omits entirely.
func spanStatus(code string) string {
	switch code {
	case "STATUS_CODE_ERROR", "2":
		return "error"
	case "STATUS_CODE_OK", "1":
		return "ok"
	default:
		return "unset"
	}
}

// normalizeID renders a span or trace id as lowercase hex.
//
// Tempo speaks two dialects: its search API returns hex, while /api/traces
// returns protojson, which encodes the id bytes as base64. Handing both to a
// client would mean the parent_span_id of one span never matches the span_id it
// points at, and a trace id from search could not be used to fetch the trace.
// Everything leaving this package is hex.
func normalizeID(s string) string {
	if s == "" {
		return ""
	}
	if isHex(s) && len(s)%2 == 0 {
		return strings.ToLower(s)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		// Neither dialect. Pass it through rather than losing it; a caller
		// comparing ids will see the mismatch, which beats an empty field.
		return s
	}
	return hex.EncodeToString(raw)
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !(ch >= '0' && ch <= '9') && !(ch >= 'a' && ch <= 'f') && !(ch >= 'A' && ch <= 'F') {
			return false
		}
	}
	return true
}
