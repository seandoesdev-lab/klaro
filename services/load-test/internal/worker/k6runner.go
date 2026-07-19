package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/klaro/load-test/internal/breaker"
	"github.com/klaro/load-test/internal/model"
)

type metricPoint struct {
	metric string
	value  float64
	step   string
	status string
	ts     time.Time
}

type k6Line struct {
	Type   string `json:"type"`
	Metric string `json:"metric"`
	Data   struct {
		Time  time.Time `json:"time"`
		Value float64   `json:"value"`
		Tags  struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"tags"`
	} `json:"data"`
}

func parseLine(b []byte) (metricPoint, bool) {
	var l k6Line
	if err := json.Unmarshal(b, &l); err != nil {
		return metricPoint{}, false
	}
	if l.Type != "Point" {
		return metricPoint{}, false
	}
	if l.Metric != "http_req_duration" && l.Metric != "http_req_failed" {
		return metricPoint{}, false
	}
	return metricPoint{
		metric: l.Metric, value: l.Data.Value,
		step: l.Data.Tags.Step, status: l.Data.Tags.Status, ts: l.Data.Time,
	}, true
}

// Aggregator accumulates request durations and error counts to compute the
// final summary.
type Aggregator struct {
	durations    []float64
	total        int
	errors       int
	stepDuration map[string]float64
	stepCount    map[string]int
}

func NewAggregator() *Aggregator {
	return &Aggregator{stepDuration: map[string]float64{}, stepCount: map[string]int{}}
}

// isError treats missing tags and 4xx/5xx statuses as failed requests.
func isError(status string) bool {
	return status == "" || strings.HasPrefix(status, "5") || strings.HasPrefix(status, "4")
}

func (a *Aggregator) Add(p metricPoint) {
	if p.metric != "http_req_duration" {
		return
	}
	a.durations = append(a.durations, p.value)
	a.total++
	if isError(p.status) {
		a.errors++
	}
	a.stepDuration[p.step] += p.value
	a.stepCount[p.step]++
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}

func (a *Aggregator) Summary(vu int) model.LoadTestResult {
	sorted := append([]float64(nil), a.durations...)
	sort.Float64s(sorted)
	var rate float64
	if a.total > 0 {
		rate = float64(a.errors) / float64(a.total)
	}
	// slowest average step is the bottleneck
	bottleneck := ""
	var worst float64
	for step, sum := range a.stepDuration {
		avg := sum / float64(a.stepCount[step])
		if avg > worst {
			worst = avg
			bottleneck = step
		}
	}
	maxVU := vu
	if rate > 0.5 {
		maxVU = 0 // degraded across the board
	}
	return model.LoadTestResult{
		RPSAvg:                 float64(a.total),
		LatencyP50:             percentile(sorted, 0.50),
		LatencyP95:             percentile(sorted, 0.95),
		LatencyP99:             percentile(sorted, 0.99),
		ErrorRate:              rate,
		MaxVUBeforeDegradation: maxVU,
		BottleneckEndpoint:     bottleneck,
	}
}

// runK6 executes k6, streaming NDJSON points to onPoint until the process
// exits or ctx is cancelled. Cancelling ctx kills k6 (abort / circuit breaker).
func runK6(ctx context.Context, k6Path, script, target string, onPoint func(metricPoint)) error {
	cmd := exec.CommandContext(ctx, k6Path, "run", "--out", "json=-", "-e", "TARGET="+target, "-")
	cmd.Stdin = strings.NewReader(script)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		if p, ok := parseLine(scanner.Bytes()); ok {
			onPoint(p)
		}
	}
	return cmd.Wait()
}

// newBreaker returns the default circuit breaker used during a run.
func newBreaker() *breaker.Breaker {
	return breaker.New(10*time.Second, 0.8, 20)
}
