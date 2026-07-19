package worker

import (
	"math"
	"testing"
)

func TestParseLineDuration(t *testing.T) {
	line := []byte(`{"type":"Point","metric":"http_req_duration","data":{"time":"2026-07-19T00:00:00Z","value":123.4,"tags":{"step":"/health","status":"200"}}}`)
	p, ok := parseLine(line)
	if !ok || p.metric != "http_req_duration" || math.Abs(p.value-123.4) > 0.001 {
		t.Fatalf("parse failed: %+v ok=%v", p, ok)
	}
	if p.step != "/health" || p.status != "200" {
		t.Fatalf("tags not parsed: %+v", p)
	}
}

func TestParseLineIgnoresNonPoint(t *testing.T) {
	if _, ok := parseLine([]byte(`{"type":"Metric","metric":"vus"}`)); ok {
		t.Fatal("should ignore non-Point")
	}
}

func TestAggregatorSummary(t *testing.T) {
	a := NewAggregator()
	// 10 requests, durations 100..1000ms, 2 of them 500 errors
	for i := 1; i <= 10; i++ {
		status := "200"
		if i > 8 {
			status = "500"
		}
		a.Add(metricPoint{metric: "http_req_duration", value: float64(i * 100), step: "/x", status: status})
	}
	s := a.Summary(5) // vu=5
	if s.ErrorRate < 0.19 || s.ErrorRate > 0.21 {
		t.Fatalf("error rate %.3f", s.ErrorRate)
	}
	if s.LatencyP50 <= 0 || s.LatencyP95 < s.LatencyP50 {
		t.Fatalf("bad percentiles: %+v", s)
	}
	if s.BottleneckEndpoint != "/x" {
		t.Fatalf("bottleneck %q", s.BottleneckEndpoint)
	}
}
