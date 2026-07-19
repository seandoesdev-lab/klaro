package model

import "testing"

func TestCanTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		ok       bool
	}{
		{StatusPending, StatusValidating, true},
		{StatusValidating, StatusQueued, true},
		{StatusValidating, StatusRejected, true},
		{StatusQueued, StatusProvisioning, true},
		{StatusProvisioning, StatusRunning, true},
		{StatusRunning, StatusAggregating, true},
		{StatusRunning, StatusAborted, true},
		{StatusAggregating, StatusCompleted, true},
		{StatusRunning, StatusFailed, true},
		{StatusCompleted, StatusRunning, false},
		{StatusPending, StatusCompleted, false},
		{StatusAborted, StatusRunning, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to); got != c.ok {
			t.Errorf("CanTransition(%s,%s)=%v want %v", c.from, c.to, got, c.ok)
		}
	}
}
