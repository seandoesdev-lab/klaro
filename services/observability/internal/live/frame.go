// Package live carries near-real-time telemetry from the Collector replica
// stream to WebSocket clients (design HOW-7, OBS-01/APM-02).
//
// Two halves meet here. Frame is the wire shape clients receive; Hub is the
// fan-out that gets it to them. The Collector speaks OTLP, so the translation
// happens once at publish time rather than once per subscriber - a hundred
// dashboards watching one org must not mean a hundred OTLP parses per flush.
package live

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Stream names a live channel. The design fixes the set (section 4.3), and it
// is an allowlist rather than free text because the name becomes part of a
// Redis channel: an unchecked value could address a channel the caller was
// never granted.
const (
	StreamMetric  = "metric"
	StreamService = "service"
)

// Streams is the accepted set.
var Streams = []string{StreamMetric, StreamService}

// ValidStream reports whether s is a known live stream.
func ValidStream(s string) bool {
	for _, known := range Streams {
		if s == known {
			return true
		}
	}
	return false
}

// Point is one measurement with its identifying labels.
type Point struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// Frame is what a WebSocket client receives (design section 4.3).
//
// TS is unix milliseconds: the browser reads it straight into a Date, and the
// live view never needs nanosecond resolution that a 2 second flush cannot
// deliver anyway.
type Frame struct {
	TS     int64   `json:"ts"`
	Stream string  `json:"stream"`
	Points []Point `json:"points"`
}

// Internal resource attributes that exist to route telemetry, not to describe
// it. They are dropped from the frame: a client already knows its own org, and
// the VictoriaMetrics tenant is plumbing it must never depend on.
var internalAttrs = map[string]bool{
	"klaro.org_id":  true,
	"vm_account_id": true,
	"vm_project_id": true,
}

// otlpValue is the subset of an OTLP AnyValue this package reads.
type otlpValue struct {
	StringValue *string  `json:"stringValue"`
	IntValue    *string  `json:"intValue"`
	DoubleValue *float64 `json:"doubleValue"`
	BoolValue   *bool    `json:"boolValue"`
}

func (v otlpValue) str() string {
	switch {
	case v.StringValue != nil:
		return *v.StringValue
	case v.IntValue != nil:
		return *v.IntValue
	case v.DoubleValue != nil:
		return strconv.FormatFloat(*v.DoubleValue, 'g', -1, 64)
	case v.BoolValue != nil:
		return strconv.FormatBool(*v.BoolValue)
	}
	return ""
}

type otlpAttr struct {
	Key   string    `json:"key"`
	Value otlpValue `json:"value"`
}

// otlpNumberPoint covers gauge and sum data points. OTLP encodes int64 as a
// JSON string, hence the *string.
type otlpNumberPoint struct {
	Attributes   []otlpAttr `json:"attributes"`
	TimeUnixNano string     `json:"timeUnixNano"`
	AsDouble     *float64   `json:"asDouble"`
	AsInt        *string    `json:"asInt"`
}

func (p otlpNumberPoint) value() (float64, bool) {
	if p.AsDouble != nil {
		return *p.AsDouble, true
	}
	if p.AsInt != nil {
		n, err := strconv.ParseFloat(*p.AsInt, 64)
		return n, err == nil
	}
	return 0, false
}

// otlpHistogramPoint is reduced to its count and sum: a live tile plots a
// number, and shipping every bucket to a browser twice a second would cost far
// more than the view is worth. Percentiles come from the Explorer, which reads
// the full histogram out of VictoriaMetrics.
type otlpHistogramPoint struct {
	Attributes   []otlpAttr `json:"attributes"`
	TimeUnixNano string     `json:"timeUnixNano"`
	Count        *string    `json:"count"`
	Sum          *float64   `json:"sum"`
}

type otlpMetric struct {
	Name  string `json:"name"`
	Gauge *struct {
		DataPoints []otlpNumberPoint `json:"dataPoints"`
	} `json:"gauge"`
	Sum *struct {
		DataPoints []otlpNumberPoint `json:"dataPoints"`
	} `json:"sum"`
	Histogram *struct {
		DataPoints []otlpHistogramPoint `json:"dataPoints"`
	} `json:"histogram"`
}

// ResourceMetrics is one OTLP resource group, the unit the control plane
// already splits an export into (one group belongs to exactly one org).
type ResourceMetrics struct {
	Resource struct {
		Attributes []otlpAttr `json:"attributes"`
	} `json:"resource"`
	ScopeMetrics []struct {
		Metrics []otlpMetric `json:"metrics"`
	} `json:"scopeMetrics"`
}

// FlattenOTLP turns one OTLP resource group into the client frame.
//
// nowMillis is passed in rather than read from the clock so the caller decides
// the frame timestamp - and so the result is testable.
func FlattenOTLP(group json.RawMessage, stream string, nowMillis int64) (Frame, error) {
	var rm ResourceMetrics
	if err := json.Unmarshal(group, &rm); err != nil {
		return Frame{}, fmt.Errorf("decode otlp resource metrics: %w", err)
	}

	base := map[string]string{}
	for _, a := range rm.Resource.Attributes {
		if !internalAttrs[a.Key] {
			base[a.Key] = a.Value.str()
		}
	}

	frame := Frame{TS: nowMillis, Stream: stream, Points: []Point{}}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			frame.Points = append(frame.Points, pointsOf(m, base)...)
		}
	}
	return frame, nil
}

func pointsOf(m otlpMetric, base map[string]string) []Point {
	var out []Point
	number := func(dps []otlpNumberPoint) {
		for _, dp := range dps {
			v, ok := dp.value()
			if !ok {
				continue // a data point with no numeric value has nothing to plot
			}
			out = append(out, Point{Labels: labelsFor(base, m.Name, dp.Attributes), Value: v})
		}
	}
	if m.Gauge != nil {
		number(m.Gauge.DataPoints)
	}
	if m.Sum != nil {
		number(m.Sum.DataPoints)
	}
	if m.Histogram != nil {
		for _, dp := range m.Histogram.DataPoints {
			if dp.Count != nil {
				if n, err := strconv.ParseFloat(*dp.Count, 64); err == nil {
					out = append(out, Point{Labels: labelsFor(base, m.Name+"_count", dp.Attributes), Value: n})
				}
			}
			if dp.Sum != nil {
				out = append(out, Point{Labels: labelsFor(base, m.Name+"_sum", dp.Attributes), Value: *dp.Sum})
			}
		}
	}
	return out
}

// labelsFor copies the resource labels rather than sharing them, so two points
// from the same resource cannot alias one map and overwrite each other.
func labelsFor(base map[string]string, name string, attrs []otlpAttr) map[string]string {
	labels := make(map[string]string, len(base)+len(attrs)+1)
	for k, v := range base {
		labels[k] = v
	}
	for _, a := range attrs {
		if !internalAttrs[a.Key] {
			labels[a.Key] = a.Value.str()
		}
	}
	labels["__name__"] = name
	return labels
}
