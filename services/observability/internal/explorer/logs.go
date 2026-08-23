package explorer

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
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
	Limit    int
}

// LogEntry is one line.
type LogEntry struct {
	// TS is unix milliseconds.
	TS      int64             `json:"ts"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Labels  map[string]string `json:"labels"`
}

// LogsPage is the response. Next is a cursor for the following page, empty when
// the window is exhausted.
type LogsPage struct {
	Data  []LogEntry `json:"data"`
	Next  string     `json:"next,omitempty"`
	Query string     `json:"query"`
}

// Validate checks the query and applies the limit ceiling.
func (q *LogsQuery) Validate(maxLimit int) error {
	if err := q.Range.Validate(); err != nil {
		return err
	}
	if !validLabelValue(q.Contains) {
		return fmt.Errorf("%w: query is too long or contains control characters", ErrInvalidQuery)
	}
	if q.Limit <= 0 || q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	return nil
}

// logQL renders the selector plus an optional literal line filter.
func logQL(orgID string, matchers []Matcher, contains string) string {
	expr := Selector("", orgID, matchers)
	if contains != "" {
		expr += " |= " + strconv.Quote(contains)
	}
	return expr
}

// lokiRangeResponse is the subset of Loki's query_range body this package maps.
type lokiRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
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

	expr := logQL(orgID, q.Filters, q.Contains)
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
			nano, err := strconv.ParseInt(v[0], 10, 64)
			if err != nil {
				continue
			}
			if oldestNano == 0 || nano < oldestNano {
				oldestNano = nano
			}
			page.Data = append(page.Data, LogEntry{
				TS:      nano / int64(time.Millisecond),
				Level:   levelOf(stream.Stream),
				Message: v[1],
				Labels:  labels,
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

func levelOf(stream map[string]string) string {
	for _, key := range levelLabels {
		if v, ok := stream[key]; ok && v != "" {
			return v
		}
	}
	return ""
}
