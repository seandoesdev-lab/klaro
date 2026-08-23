package audit

import (
	"errors"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestEntryValidate(t *testing.T) {
	const uid = "00000000-0000-0000-0000-000000000009"

	ok := []Entry{
		{Action: ActionKeyIssue},
		{Action: ActionRuleCreate, ActorUserID: ptr(uid), ResourceID: ptr(uid), ResourceType: "alert_rule"},
	}
	for _, e := range ok {
		if err := e.validate(); err != nil {
			t.Errorf("validate(%+v) = %v, want nil", e, err)
		}
	}

	bad := []struct {
		name string
		e    Entry
	}{
		{"missing action", Entry{}},
		{"actor not uuid", Entry{Action: ActionKeyIssue, ActorUserID: ptr("someone")}},
		{"resource not uuid", Entry{Action: ActionKeyIssue, ResourceID: ptr("123")}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.e.validate(); !errors.Is(err, ErrInvalidEntry) {
				t.Fatalf("err = %v, want ErrInvalidEntry", err)
			}
		})
	}
}

// org_id must come from the transaction GUC, never from a caller-supplied
// parameter, so an audit row can never be filed against another tenant.
func TestInsertDerivesOrgFromSessionGUC(t *testing.T) {
	if !strings.Contains(insertSQL, "current_setting('app.current_org')::uuid") {
		t.Fatalf("insertSQL must derive org_id from app.current_org:\n%s", insertSQL)
	}
	if strings.Contains(insertSQL, "$6") {
		t.Error("insertSQL should not take org_id as a bind parameter")
	}
}

func TestNullStr(t *testing.T) {
	if nullStr("") != nil {
		t.Error("empty string should map to NULL")
	}
	if got := nullStr("alert_rule"); got == nil || *got != "alert_rule" {
		t.Errorf("nullStr = %v", got)
	}
}
