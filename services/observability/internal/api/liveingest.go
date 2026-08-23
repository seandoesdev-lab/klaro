package api

import (
	"encoding/json"
	"fmt"

	"github.com/klaro/observability/internal/platform/db"
)

// otlpOrgAttribute is the resource attribute the klarotenant processor stamps
// on every batch it forwards. It is the control-plane-issued org, never
// anything the SDK chose.
const otlpOrgAttribute = "klaro.org_id"

// otlpMetricStream is the live stream name OTLP metric replicas land on.
const otlpMetricStream = "metric"

// otlpMetrics is the slice of the OTLP/JSON metrics envelope this endpoint
// needs.
//
// Hand-rolled rather than pulled from the OTel proto packages: the control
// plane only has to find the org on each resource and pass the group through
// untouched, and taking the collector module as a dependency here would drag
// the whole pdata tree into a service that never inspects a datapoint.
type otlpMetrics struct {
	ResourceMetrics []json.RawMessage `json:"resourceMetrics"`
}

// otlpResourceHeader reads just the resource attributes of one group.
type otlpResourceHeader struct {
	Resource struct {
		Attributes []struct {
			Key   string `json:"key"`
			Value struct {
				StringValue *string `json:"stringValue"`
			} `json:"value"`
		} `json:"attributes"`
	} `json:"resource"`
}

// liveBatch is one publishable unit: a group of OTLP resource metrics together
// with the org it belongs to.
type liveBatch struct {
	OrgID  string          `json:"org_id"`
	Stream string          `json:"stream"`
	Group  json.RawMessage `json:"resourceMetrics"`
}

// splitOTLPByOrg turns an OTLP/JSON metrics payload into one batch per resource
// group, keyed by the org stamped on that resource.
//
// One collector export can carry several orgs, because the gateway batches
// across tenants. Publishing the whole payload to one org would leak every
// other tenant in it, so the split happens here rather than at the WebSocket.
func splitOTLPByOrg(body []byte) ([]liveBatch, error) {
	var env otlpMetrics
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode otlp metrics: %w", err)
	}
	out := make([]liveBatch, 0, len(env.ResourceMetrics))
	for _, group := range env.ResourceMetrics {
		orgID, err := orgOfResource(group)
		if err != nil {
			return nil, err
		}
		out = append(out, liveBatch{OrgID: orgID, Stream: otlpMetricStream, Group: group})
	}
	return out, nil
}

// orgOfResource extracts the stamped org, refusing a group that has none: an
// unattributed batch cannot be published to anyone without guessing.
func orgOfResource(group json.RawMessage) (string, error) {
	var hdr otlpResourceHeader
	if err := json.Unmarshal(group, &hdr); err != nil {
		return "", fmt.Errorf("decode otlp resource: %w", err)
	}
	for _, attr := range hdr.Resource.Attributes {
		if attr.Key != otlpOrgAttribute || attr.Value.StringValue == nil {
			continue
		}
		orgID := *attr.Value.StringValue
		if !db.ValidOrgID(orgID) {
			return "", fmt.Errorf("resource attribute %s is not a uuid: %q", otlpOrgAttribute, orgID)
		}
		return orgID, nil
	}
	return "", fmt.Errorf("resource has no %s attribute", otlpOrgAttribute)
}
