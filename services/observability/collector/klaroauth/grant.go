package klaroauth

import "go.opentelemetry.io/collector/client"

// Metadata keys this extension puts on the request context.
//
// They are how the rest of the pipeline learns the tenant. headerssetter reads
// MetadataScopeOrgID with from_context to stamp X-Scope-OrgID on the Tempo and
// Loki exports; the klarotenant processor reads MetadataVMAccountID to label
// metrics for the VictoriaMetrics tenant.
const (
	MetadataScopeOrgID  = "x-scope-orgid"
	MetadataVMAccountID = "klaro-vm-account-id"
	MetadataOrgID       = "klaro-org-id"
	// MetadataQuotaOverage marks telemetry from an org that is over its plan.
	// It never blocks the data: the confirmed policy (design section 7-1) is to
	// keep accepting and bill the overage.
	MetadataQuotaOverage = "klaro-quota-overage"
)

// quota mirrors the subset of the control plane quota snapshot the gateway uses.
type quota struct {
	ActiveHosts    int     `json:"active_hosts"`
	IngestGB       float64 `json:"ingest_gb"`
	HostsExceeded  bool    `json:"hosts_exceeded"`
	IngestExceeded bool    `json:"ingest_exceeded"`
	Overage        bool    `json:"overage"`
}

// grant is the control plane answer to one authz question. It mirrors
// ingestkey.Grant on the server side.
type grant struct {
	OrgID        string `json:"org_id"`
	KeyID        string `json:"key_id"`
	VMAccountID  uint32 `json:"vm_account_id"`
	VMTenantPath string `json:"vm_tenant_path"`
	ScopeOrgID   string `json:"x_scope_org_id"`
	Quota        quota  `json:"quota"`
	CacheTTLSec  int    `json:"cache_ttl_sec"`
}

// authData exposes the grant to any component that inspects client.Info.Auth.
type authData struct{ g grant }

// GetAttribute implements client.AuthData.
func (a authData) GetAttribute(name string) any {
	switch name {
	case "org_id":
		return a.g.OrgID
	case "key_id":
		return a.g.KeyID
	case "vm_account_id":
		return a.g.VMAccountID
	case "x_scope_org_id":
		return a.g.ScopeOrgID
	case "quota_overage":
		return a.g.Quota.Overage
	default:
		return nil
	}
}

// GetAttributeNames implements client.AuthData.
func (a authData) GetAttributeNames() []string {
	return []string{"org_id", "key_id", "vm_account_id", "x_scope_org_id", "quota_overage"}
}

var _ client.AuthData = authData{}
