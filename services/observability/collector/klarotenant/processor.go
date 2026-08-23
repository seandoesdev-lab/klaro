package klarotenant

import (
	"context"
	"strconv"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/klaro/observability/collector/klaroauth"
)

// tenant is the identity resolved for one request.
type tenant struct {
	orgID     string
	accountID string
}

// tenantFrom reads the identity klaroauth left on the context.
//
// It reads metadata rather than client.Info.Auth because the batch processor,
// when configured with metadata_keys, preserves metadata across the batching
// boundary - Auth does not survive it.
func tenantFrom(ctx context.Context) (tenant, bool) {
	md := client.FromContext(ctx).Metadata
	org := first(md.Get(klaroauth.MetadataOrgID))
	account := first(md.Get(klaroauth.MetadataVMAccountID))
	if org == "" || account == "" || account == "0" {
		// AccountID 0 is the VictoriaMetrics default account: treating it as a
		// tenant would merge this data into the shared bucket.
		return tenant{}, false
	}
	return tenant{orgID: org, accountID: account}, true
}

func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

type stamper struct{ cfg Config }

// stamp overwrites the tenant attributes on one resource.
func (p stamper) stamp(attrs pcommon.Map, t tenant) {
	attrs.PutStr(AttrOrgID, t.orgID)
	attrs.PutStr(AttrVMAccountID, t.accountID)
	attrs.PutStr(AttrVMProjectID, strconv.FormatUint(uint64(p.cfg.VMProjectID), 10))
}

// clear removes tenant attributes from a batch that has no resolved tenant, so
// a client cannot route itself by setting them.
func clear(attrs pcommon.Map) {
	attrs.Remove(AttrOrgID)
	attrs.Remove(AttrVMAccountID)
	attrs.Remove(AttrVMProjectID)
}

func (p stamper) processMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	t, ok := tenantFrom(ctx)
	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		attrs := rms.At(i).Resource().Attributes()
		if ok {
			p.stamp(attrs, t)
		} else {
			clear(attrs)
		}
	}
	if !ok && p.cfg.requireTenant() {
		return md, errNoTenant
	}
	return md, nil
}

func (p stamper) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	t, ok := tenantFrom(ctx)
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		attrs := rss.At(i).Resource().Attributes()
		if ok {
			p.stamp(attrs, t)
		} else {
			clear(attrs)
		}
	}
	if !ok && p.cfg.requireTenant() {
		return td, errNoTenant
	}
	return td, nil
}

func (p stamper) processLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	t, ok := tenantFrom(ctx)
	rls := ld.ResourceLogs()
	for i := 0; i < rls.Len(); i++ {
		attrs := rls.At(i).Resource().Attributes()
		if ok {
			p.stamp(attrs, t)
		} else {
			clear(attrs)
		}
	}
	if !ok && p.cfg.requireTenant() {
		return ld, errNoTenant
	}
	return ld, nil
}
