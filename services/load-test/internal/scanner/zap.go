package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/klaro/load-test/internal/model"
)

// ZAP drives an OWASP ZAP daemon over its REST API (design #3). The daemon is a
// separate Compose service (zaproxy/zap-stable). baseline runs the spider then a
// passive read of alerts; active additionally runs the ascan attack phase, which
// is only reachable behind the [SC-01] verified-domain gate.
type ZAP struct {
	Addr   string // e.g. http://zap:8090
	Client *http.Client
}

func (z ZAP) Name() string { return "zap" }

func (z ZAP) client() *http.Client {
	if z.Client != nil {
		return z.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

// Scan runs spider → (active? ascan) → collect alerts and normalizes them. Honors
// ctx for the overall deadline (design #4: baseline 600s / active 900s).
func (z ZAP) Scan(ctx context.Context, targetURL string, mode Mode) ([]model.ScanFinding, error) {
	if z.Addr == "" {
		return nil, fmt.Errorf("zap: ZAP_ADDR not configured")
	}
	if _, err := z.action(ctx, "/JSON/spider/action/scan/", url.Values{"url": {targetURL}, "recurse": {"true"}}); err != nil {
		return nil, fmt.Errorf("zap spider start: %w", err)
	}
	if err := z.poll(ctx, "/JSON/spider/view/status/", nil); err != nil {
		return nil, fmt.Errorf("zap spider: %w", err)
	}
	if mode == ModeActive {
		if _, err := z.action(ctx, "/JSON/ascan/action/scan/", url.Values{"url": {targetURL}, "recurse": {"true"}}); err != nil {
			return nil, fmt.Errorf("zap ascan start: %w", err)
		}
		if err := z.poll(ctx, "/JSON/ascan/view/status/", nil); err != nil {
			return nil, fmt.Errorf("zap ascan: %w", err)
		}
	}
	raw, err := z.get(ctx, "/JSON/core/view/alerts/", url.Values{"baseurl": {targetURL}})
	if err != nil {
		return nil, fmt.Errorf("zap alerts: %w", err)
	}
	return parseZAPAlerts(raw)
}

func (z ZAP) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	u := strings.TrimRight(z.Addr, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := z.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zap %s status %d", path, resp.StatusCode)
	}
	return buf, nil
}

func (z ZAP) action(ctx context.Context, path string, q url.Values) ([]byte, error) {
	return z.get(ctx, path, q)
}

// poll repeats a status view until it reports 100(%) or ctx is done.
func (z ZAP) poll(ctx context.Context, path string, q url.Values) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		raw, err := z.get(ctx, path, q)
		if err != nil {
			return err
		}
		var s struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &s)
		if n, err := strconv.Atoi(strings.TrimSpace(s.Status)); err == nil && n >= 100 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// zap alerts JSON shape (subset of /JSON/core/view/alerts/).
type zapAlertsOutput struct {
	Alerts []zapAlert `json:"alerts"`
}
type zapAlert struct {
	PluginID   string `json:"pluginId"`
	AlertRef   string `json:"alertRef"`
	Name       string `json:"name"`
	Risk       string `json:"risk"`
	Confidence string `json:"confidence"`
	URL        string `json:"url"`
	Param      string `json:"param"`
	Evidence   string `json:"evidence"`
	CWEID      string `json:"cweid"`
	Solution   string `json:"solution"`
	Reference  string `json:"reference"`
}

func parseZAPAlerts(data []byte) ([]model.ScanFinding, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var o zapAlertsOutput
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("zap parse: %w", err)
	}
	out := make([]model.ScanFinding, 0, len(o.Alerts))
	for _, a := range o.Alerts {
		pid := a.PluginID
		if pid == "" {
			pid = a.AlertRef
		}
		ruleID := "zap." + pid
		sev := mapZAPRisk(a.Risk)
		f := openFinding(ruleID, sev, truncate(a.Name, 300))
		f.CWE = strPtr(a.CWEID)
		f.Confidence = strPtr(strings.ToLower(a.Confidence))
		f.FindingHash = model.FindingHashParts(ruleID, a.URL, a.Param)
		ev, _ := json.Marshal(map[string]any{
			"url":       a.URL,
			"param":     a.Param,
			"evidence":  a.Evidence,
			"solution":  a.Solution,
			"reference": a.Reference,
		})
		f.Evidence = ev
		out = append(out, f)
	}
	return out, nil
}
