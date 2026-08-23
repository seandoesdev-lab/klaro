package klarotenant

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/client"
	"go.opentelemetry.io/collector/pdata/pmetric"

	"github.com/klaro/observability/collector/klaroauth"
)

const testOrg = "00000000-0000-0000-0000-0000000000aa"

// tenantCtx mimics what klaroauth leaves on the request context.
func tenantCtx(org, account string) context.Context {
	md := map[string][]string{}
	if org != "" {
		md[klaroauth.MetadataOrgID] = []string{org}
	}
	if account != "" {
		md[klaroauth.MetadataVMAccountID] = []string{account}
	}
	return client.NewContext(context.Background(), client.Info{Metadata: client.NewMetadata(md)})
}

// metricsWith builds a one-resource batch carrying the given attributes.
func metricsWith(attrs map[string]string) pmetric.Metrics {
	md := pmetric.NewMetrics()
	rm := md.ResourceMetrics().AppendEmpty()
	for k, v := range attrs {
		rm.Resource().Attributes().PutStr(k, v)
	}
	rm.ScopeMetrics().AppendEmpty().Metrics().AppendEmpty().SetName("http.server.duration")
	return md
}

func resourceAttr(t *testing.T, md pmetric.Metrics, key string) (string, bool) {
	t.Helper()
	v, ok := md.ResourceMetrics().At(0).Resource().Attributes().Get(key)
	if !ok {
		return "", false
	}
	return v.Str(), true
}

func TestMetricsGetTheResolvedTenant(t *testing.T) {
	p := stamper{cfg: Config{}}
	out, err := p.processMetrics(tenantCtx(testOrg, "7"), metricsWith(nil))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		AttrOrgID:       testOrg,
		AttrVMAccountID: "7",
		AttrVMProjectID: "0",
	} {
		got, ok := resourceAttr(t, out, key)
		if !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", key, got, ok, want)
		}
	}
}

// The attack this exists to stop: an SDK that labels itself into another org.
// VictoriaMetrics routes multitenant writes by these labels, so a client-set
// value would be a cross-tenant write that no Postgres policy can see.
func TestClientSuppliedTenantIsOverwritten(t *testing.T) {
	p := stamper{cfg: Config{}}
	spoofed := metricsWith(map[string]string{
		AttrVMAccountID: "999",
		AttrOrgID:       "00000000-0000-0000-0000-0000000000bb",
	})

	out, err := p.processMetrics(tenantCtx(testOrg, "7"), spoofed)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := resourceAttr(t, out, AttrVMAccountID); got != "7" {
		t.Errorf("vm_account_id = %q, the client kept its own value", got)
	}
	if got, _ := resourceAttr(t, out, AttrOrgID); got != testOrg {
		t.Errorf("klaro.org_id = %q, the client kept its own value", got)
	}
}

// With no resolved tenant the batch must be refused, and the spoofed labels
// stripped on the way out, so nothing can reach the shared default account.
func TestUntenantedBatchIsRefusedAndStripped(t *testing.T) {
	p := stamper{cfg: Config{}}
	spoofed := metricsWith(map[string]string{AttrVMAccountID: "999"})

	out, err := p.processMetrics(context.Background(), spoofed)
	if err == nil {
		t.Fatal("a batch with no tenant was accepted")
	}
	if _, ok := resourceAttr(t, out, AttrVMAccountID); ok {
		t.Error("the client-supplied vm_account_id survived")
	}
}

// AccountID 0 is the VictoriaMetrics default account, so it is not a tenant.
func TestAccountZeroIsNotATenant(t *testing.T) {
	p := stamper{cfg: Config{}}
	if _, err := p.processMetrics(tenantCtx(testOrg, "0"), metricsWith(nil)); err == nil {
		t.Error("AccountID 0 was accepted as a tenant")
	}
}

// Turning the guard off is allowed but has to be explicit.
func TestRequireTenantCanBeDisabled(t *testing.T) {
	off := false
	p := stamper{cfg: Config{RequireTenant: &off}}
	out, err := p.processMetrics(context.Background(), metricsWith(map[string]string{AttrVMAccountID: "999"}))
	if err != nil {
		t.Fatalf("err = %v, want the batch to pass", err)
	}
	// Even then, a client must not be able to choose its own tenant.
	if _, ok := resourceAttr(t, out, AttrVMAccountID); ok {
		t.Error("the client-supplied vm_account_id survived")
	}
}
