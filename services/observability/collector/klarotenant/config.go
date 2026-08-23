// Package klarotenant stamps the control-plane-issued tenant onto telemetry.
//
// VictoriaMetrics routes a multitenant write by the vm_account_id label rather
// than by a header, so the tenant has to travel as data, not as request
// metadata. This processor copies the identity that klaroauth put on the
// request context onto every resource in the batch, and strips any tenant
// attribute the SDK supplied - otherwise a client could label itself into
// another org.
package klarotenant

import (
	"errors"
	"fmt"
)

// Resource attributes this processor owns. Anything arriving with these set is
// overwritten, never trusted.
const (
	AttrVMAccountID = "vm_account_id"
	AttrVMProjectID = "vm_project_id"
	AttrOrgID       = "klaro.org_id"
)

// Config is the klarotenant processor configuration.
type Config struct {
	// VMProjectID is the VictoriaMetrics project inside the account. klaro keys
	// tenancy on the org alone, so it is a constant.
	VMProjectID uint32 `mapstructure:"vm_project_id"`

	// RequireTenant drops a batch that arrives without a resolved tenant.
	//
	// Default true, and it should stay true: unlabelled metrics land in the
	// VictoriaMetrics default account, which is a bucket shared by every org.
	// Losing a batch is recoverable; silently merging tenants is not.
	RequireTenant *bool `mapstructure:"require_tenant"`
}

// ErrInvalidConfig is returned by Validate.
var ErrInvalidConfig = errors.New("invalid klarotenant configuration")

// Validate implements component.ConfigValidator.
func (c *Config) Validate() error {
	if c.RequireTenant != nil && !*c.RequireTenant {
		// Allowed, but the operator has to have typed it.
		return nil
	}
	return nil
}

func (c Config) requireTenant() bool {
	return c.RequireTenant == nil || *c.RequireTenant
}

// errNoTenant is returned when a batch carries no resolved tenant.
var errNoTenant = fmt.Errorf("%w: batch has no resolved klaro tenant", ErrInvalidConfig)
