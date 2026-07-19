package model

type Status string

const (
	StatusPending      Status = "pending"
	StatusValidating   Status = "validating"
	StatusQueued       Status = "queued"
	StatusProvisioning Status = "provisioning"
	StatusRunning      Status = "running"
	StatusAggregating  Status = "aggregating"
	StatusCompleted    Status = "completed"
	StatusFailed       Status = "failed"
	StatusAborted      Status = "aborted"
	StatusRejected     Status = "rejected"
)

var allowed = map[Status]map[Status]bool{
	StatusPending:      {StatusValidating: true},
	StatusValidating:   {StatusQueued: true, StatusRejected: true},
	StatusQueued:       {StatusProvisioning: true, StatusAborted: true},
	StatusProvisioning: {StatusRunning: true, StatusFailed: true, StatusAborted: true},
	StatusRunning:      {StatusAggregating: true, StatusAborted: true, StatusFailed: true},
	StatusAggregating:  {StatusCompleted: true, StatusFailed: true},
}

// CanTransition reports whether moving from->to is a legal state change.
func CanTransition(from, to Status) bool {
	return allowed[from][to]
}
