package model

import "testing"

func TestFilterSlowSpansBoundary(t *testing.T) {
	spans := []ApmSpan{
		{Name: "fast", DurationMs: 42},
		{Name: "exact", DurationMs: 3000},
		{Name: "slow", DurationMs: 3200},
		{Name: "just-under", DurationMs: 2999.9},
	}
	got := FilterSlowSpans(spans, 3000)
	if len(got) != 2 {
		t.Fatalf("expected 2 slow spans (>=3000), got %d", len(got))
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["exact"] || !names["slow"] {
		t.Errorf("boundary wrong: %v", names)
	}
	if names["fast"] || names["just-under"] {
		t.Errorf("under-threshold span leaked: %v", names)
	}
}

func TestFilterSlowSpansEmpty(t *testing.T) {
	if got := FilterSlowSpans(nil, 3000); len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

func TestValidAgentLanguage(t *testing.T) {
	for _, l := range []AgentLanguage{AgentNodeJS, AgentSpringBoot, AgentFastAPI} {
		if !ValidAgentLanguage(l) {
			t.Errorf("%s should be valid", l)
		}
	}
	if ValidAgentLanguage("ruby") {
		t.Error("ruby must be invalid")
	}
}

func TestNewIngestTokenUnique(t *testing.T) {
	a, b := NewIngestToken(), NewIngestToken()
	if a == b {
		t.Error("ingest tokens must differ")
	}
	if len(a) < 10 {
		t.Errorf("token too short: %s", a)
	}
}
