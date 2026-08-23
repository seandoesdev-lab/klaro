package usage

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const orgA = "00000000-0000-0000-0000-0000000000aa"

func TestReportValidate(t *testing.T) {
	good := Report{
		OrgID:   orgA,
		Signals: map[string]SignalUsage{"metrics": {Bytes: 100, Items: 4}},
		Hosts:   []Host{{Ident: "pod-uid-1", Service: "checkout", Env: "prod"}},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a valid report was rejected: %v", err)
	}

	bad := map[string]Report{
		"no org":         {Signals: map[string]SignalUsage{"metrics": {Bytes: 1}}},
		"org not uuid":   {OrgID: "acme"},
		"unknown signal": {OrgID: orgA, Signals: map[string]SignalUsage{"profiles": {Bytes: 1}}},
		"empty host":     {OrgID: orgA, Hosts: []Host{{Ident: ""}}},
		"huge host id":   {OrgID: orgA, Hosts: []Host{{Ident: strings.Repeat("x", 513)}}},
	}
	for name, r := range bad {
		t.Run(name, func(t *testing.T) {
			if err := r.Validate(); !errors.Is(err, ErrInvalidReport) {
				t.Errorf("err = %v, want ErrInvalidReport", err)
			}
		})
	}
}

// A gateway flush covers a short window, so a report naming tens of thousands
// of hosts is a bug or an attack rather than a customer.
func TestReportBoundsHostCount(t *testing.T) {
	hosts := make([]Host, maxHostsPerReport+1)
	for i := range hosts {
		hosts[i] = Host{Ident: "h"}
	}
	if err := (Report{OrgID: orgA, Hosts: hosts}).Validate(); !errors.Is(err, ErrInvalidReport) {
		t.Errorf("err = %v, want ErrInvalidReport", err)
	}
}

// Billing reads this payload, so its shape is part of the contract.
func TestEventSerialisation(t *testing.T) {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(Event{
		OrgID:       orgA,
		Meter:       MeterIngestGB,
		Quantity:    1.5,
		Period:      Period{Start: start, End: start.AddDate(0, 1, 0)},
		Computation: map[string]any{"basis": "sum of ingested_bytes"},
		At:          start,
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Meter    string  `json:"meter"`
		Quantity float64 `json:"quantity"`
		Period   struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"period"`
		Computation map[string]any `json:"computation"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Meter != MeterIngestGB || decoded.Quantity != 1.5 {
		t.Errorf("decoded = %+v", decoded)
	}
	if !decoded.Period.Start.Equal(start) || !decoded.Period.End.Equal(start.AddDate(0, 1, 0)) {
		t.Errorf("period = %+v", decoded.Period)
	}
	// The computation travels with the number: a customer disputing an invoice
	// deserves better than "the meter said so".
	if decoded.Computation["basis"] == nil {
		t.Error("computation was dropped")
	}
}

func TestMeterNames(t *testing.T) {
	// The discriminator Billing switches on (design section 4.5).
	if MeterHosts != "observability_hosts" || MeterIngestGB != "observability_ingest_gb" {
		t.Errorf("meter names changed: %q %q", MeterHosts, MeterIngestGB)
	}
	if EmitSubject != "klaro.usage.emitted" {
		t.Errorf("subject = %q", EmitSubject)
	}
}
