package explorer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LogsQuery is the structured form of GET /obs/logs [OBS-05].
type LogsQuery struct {
	Range   TimeRange
	Filters []Matcher
	// Contains is a literal substring the log line must include. It is not a
	// regex and not LogQL: a caller-supplied pipeline could carry a label
	// matcher stage, and the point of this package is that it cannot.
	Contains string
	// TraceID narrows the query to the lines emitted inside one trace.
	//
	// This is the primary correlation key (sdk/SDK_CONTRACT.md section 12). It
	// becomes a label filter over structured metadata rather than part of the
	// stream selector, because trace_id is per-line and unbounded in
	// cardinality - indexing it as a stream label would mean one Loki stream
	// per request.
	TraceID string
	Limit   int
}

// LogEntry is one line.
type LogEntry struct {
	// TS is unix milliseconds.
	TS      int64  `json:"ts"`
	Level   string `json:"level"`
	Message string `json:"message"`
	// TraceID and SpanID are the correlation keys, promoted out of Loki's
	// structured metadata into named fields.
	//
	// Promoted rather than left among the labels because they are not labels: a
	// client links a line to a trace and to one span inside it, and leaving
	// them in the map would make every caller re-derive which key carries that.
	TraceID string            `json:"trace_id,omitempty"`
	SpanID  string            `json:"span_id,omitempty"`
	Labels  map[string]string `json:"labels"`
}

// LogsPage is the response. Next is a cursor for the following page, empty when
// the window is exhausted.
type LogsPage struct {
	Data  []LogEntry `json:"data"`
	Next  string     `json:"next,omitempty"`
	Query string     `json:"query"`
}

// Structured metadata keys carrying the correlation keys. Loki's OTLP receiver
// writes the log record's trace context under these names.
const (
	traceIDKey = "trace_id"
	spanIDKey  = "span_id"
)

// Validate checks the query and applies the limit ceiling.
func (q *LogsQuery) Validate(maxLimit int) error {
	if err := q.Range.Validate(); err != nil {
		return err
	}
	if !validLabelValue(q.Contains) {
		return fmt.Errorf("%w: query is too long or contains control characters", ErrInvalidQuery)
	}
	if q.TraceID != "" && !ValidTraceID(q.TraceID) {
		return fmt.Errorf("%w: trace id must be 16-32 hex characters", ErrInvalidQuery)
	}
	if q.Limit <= 0 || q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return nil
}

// logQL renders the selector plus an optional literal line filter and an
// optional correlation filter.
//
// The trace filter is a label filter stage, which is the only form that can
// read structured metadata. Interpolating it is safe because Validate has
// already established the value is hex; the quoting is belt and braces.
func logQL(orgID string, matchers []Matcher, contains, traceID string) string {
	expr := Selector("", orgID, matchers)
	if contains != "" {
		expr += " |= " + strconv.Quote(contains)
	}
	if traceID != "" {
		expr += " | " + traceIDKey + " = " + strconv.Quote(strings.ToLower(traceID))
	}
	return expr
}

// lokiValue is one [ts, line] pair, optionally followed by structured metadata.
//
// A custom unmarshaller rather than a fixed-size array because the third
// element is where the correlation keys live, and it is absent whenever a line
// has no structured metadata at all. Decoding into [2]string would discard it
// silently, which is the difference between a working span-to-logs link and one
// that is always empty.
type lokiValue struct {
	TS   string
	Line string
	Meta map[string]string
}

// UnmarshalJSON reads Loki's positional value tuple.
func (v *lokiValue) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if len(raw) < 2 {
		return fmt.Errorf("loki value has %d elements, want at least 2", len(raw))
	}
	if err := json.Unmarshal(raw[0], &v.TS); err != nil {
		return err
	}
	if err := json.Unmarshal(raw[1], &v.Line); err != nil {
		return err
	}
	if len(raw) > 2 {
		// Best effort: unreadable metadata costs the correlation keys for that
		// one line, not the whole page.
		_ = json.Unmarshal(raw[2], &v.Meta)
	}
	return nil
}

// lokiRangeResponse is the subset of Loki's query_range body this package maps.
type lokiRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values []lokiValue       `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// levelLabels are the places a level can hide, most specific first. OTLP sets
// severity_text; Loki's own detection sets detected_level.
var levelLabels = []string{"severity_text", "level", "detected_level"}

// QueryLogs reads the org's logs out of Loki [OBS-05].
//
// Two things scope it: the X-Scope-OrgID header, which Loki enforces because
// auth_enabled is on, and the injected klaro_org_id matcher in the selector.
func (c *Client) QueryLogs(ctx context.Context, orgID string, q LogsQuery) (LogsPage, error) {
	if !c.cfg.Configured(c.cfg.LokiURL) {
		return LogsPage{}, fmt.Errorf("%w: no logs backend configured", ErrBackend)
	}
	if err := q.Validate(c.cfg.MaxLimit); err != nil {
		return LogsPage{}, err
	}
	headers, err := c.scopeFor(ctx, orgID)
	if err != nil {
		return LogsPage{}, err
	}
	// Logs have no rollup either: Loki offers sampling and retention, not
	// downsampling. The window is narrowed to what the plan keeps.
	q.Range, _ = clamp(q.Range, c.retentionFor(ctx, orgID).Logs, time.Now())

	expr := logQL(orgID, q.Filters, q.Contains, q.TraceID)
	params := url.Values{}
	params.Set("query", expr)
	params.Set("start", strconv.FormatInt(q.Range.From.UnixNano(), 10))
	params.Set("end", strconv.FormatInt(q.Range.To.UnixNano(), 10))
	params.Set("limit", strconv.Itoa(q.Limit))
	// Newest first: a log view opens on what just happened.
	params.Set("direction", "backward")

	var raw lokiRangeResponse
	if err := c.getJSON(ctx, join(c.cfg.LokiURL, "/loki/api/v1/query_range", params), headers, &raw); err != nil {
		return LogsPage{}, err
	}
	if raw.Status != "" && raw.Status != "success" {
		return LogsPage{}, fmt.Errorf("%w: logs query returned status %q", ErrBackend, raw.Status)
	}

	page := LogsPage{Data: []LogEntry{}, Query: expr}
	oldestNano := int64(0)
	for _, stream := range raw.Data.Result {
		labels := stripInternalLabels(stream.Stream)
		for _, v := range stream.Values {
			nano, err := strconv.ParseInt(v.TS, 10, 64)
			if err != nil {
				continue
			}
			if oldestNano == 0 || nano < oldestNano {
				oldestNano = nano
			}
			page.Data = append(page.Data, LogEntry{
				TS:      nano / int64(time.Millisecond),
				Level:   levelOf(stream.Stream, v.Meta),
				Message: v.Line,
				TraceID: normalizeID(v.Meta[traceIDKey]),
				SpanID:  normalizeID(v.Meta[spanIDKey]),
				Labels:  mergeMeta(labels, v.Meta),
			})
		}
	}

	// Loki returns one block per stream; the caller wants one merged, ordered
	// view, and the ordering is what makes the cursor below meaningful.
	sort.SliceStable(page.Data, func(i, j int) bool { return page.Data[i].TS > page.Data[j].TS })

	// A full page means there is probably more. The cursor is the oldest
	// nanosecond seen, which the caller passes back as `to` - Loki's own
	// pagination model, exposed without inventing a token format.
	if len(page.Data) >= q.Limit && oldestNano > 0 {
		page.Next = strconv.FormatInt(oldestNano, 10)
	}
	return page, nil
}

// mergeMeta folds a line's structured metadata onto its stream labels.
//
// The stream map is shared by every line in the block, so it is copied rather
// than written through - otherwise one line's metadata would show up on all of
// them. The promoted correlation keys are left out: they have named fields
// already, and repeating them would have every client render them twice.
func mergeMeta(labels, meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return labels
	}
	out := make(map[string]string, len(labels)+len(meta))
	for k, v := range labels {
		out[k] = v
	}
	for k, v := range meta {
		if k == traceIDKey || k == spanIDKey || ReservedLabel(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// levelOf finds the severity, preferring the line's own structured metadata
// over the stream label it shares with every other line in the block.
func levelOf(stream, meta map[string]string) string {
	for _, key := range levelLabels {
		if v, ok := meta[key]; ok && v != "" {
			return v
		}
	}
	for _, key := range levelLabels {
		if v, ok := stream[key]; ok && v != "" {
			return v
		}
	}
	return ""
}
