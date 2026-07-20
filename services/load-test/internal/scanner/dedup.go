package scanner

import (
	"strings"

	"github.com/klaro/load-test/internal/model"
)

// zapTopicSuppress maps a ZAP pluginId to the header-analysis rule_ids it makes
// redundant (design #10 / §6.5). When ZAP reports a topic, its richer finding
// (request/response evidence) wins and the offline header finding is dropped.
var zapTopicSuppress = map[string][]string{
	"10035": {"dast.header.missing-hsts", "dast.header.weak-hsts"},                               // HSTS
	"10038": {"dast.header.missing-csp"},                                                         // CSP
	"10020": {"dast.header.missing-x-frame-options"},                                             // X-Frame-Options
	"10021": {"dast.header.missing-x-content-type-options"},                                      // X-Content-Type-Options
	"10036": {"dast.header.server-version-disclosure", "dast.header.server-software-disclosure"}, // Server disclosure
}

// DedupDAST merges header-analysis findings with ZAP findings, giving ZAP priority
// on overlapping topics: any header rule_id covered by a reported ZAP pluginId is
// suppressed. Order: surviving header findings first, then ZAP findings.
func DedupDAST(header, zap []model.ScanFinding) []model.ScanFinding {
	suppress := map[string]bool{}
	for _, z := range zap {
		pid := strings.TrimPrefix(z.RuleID, "zap.")
		for _, ruleID := range zapTopicSuppress[pid] {
			suppress[ruleID] = true
		}
	}
	out := make([]model.ScanFinding, 0, len(header)+len(zap))
	for _, h := range header {
		if suppress[h.RuleID] {
			continue
		}
		out = append(out, h)
	}
	out = append(out, zap...)
	return out
}
