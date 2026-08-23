package api

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/klaro/observability/internal/live"
	"github.com/klaro/observability/internal/platform/db"
)

// otlpOrgAttribute is the resource attribute the klarotenant processor stamps
// on every batch it forwards. It is the control-plane-issued org, never
// anything the SDK chose.
const otlpOrgAttribute = "klaro.org_id"

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

// orgFrame is one publishable unit: a live frame together with the org whose
// channel it belongs on.
type orgFrame struct {
	OrgID string
	Frame live.Frame
}

// parseLiveFrames turns a live-ingest body into the frames to publish.
//
// Two shapes are accepted. The Collector exports OTLP/JSON, because that is
// what an otlphttp exporter emits and adding a bespoke exporter to the gateway
// to reshape it would be a component to maintain for no benefit. The flat
// frame shape stays supported because it is what tests and curl send, and what
// a future non-OTLP producer would use.
//
// Translation happens here rather than at the WebSocket: a hundred dashboards
// watching one org must not mean a hundred OTLP parses per Collector flush.
func parseLiveFrames(body []byte, nowMillis int64) ([]orgFrame, error) {
	if len(body) == 0 {
		return nil, errors.New("body must be a JSON object")
	}
	var probe struct {
		OrgID           string            `json:"org_id"`
		Stream          string            `json:"stream"`
		Points          []live.Point      `json:"points"`
		TS              int64             `json:"ts"`
		ResourceMetrics []json.RawMessage `json:"resourceMetrics"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, fmt.Errorf("body must be a JSON object: %v", err)
	}

	if len(probe.ResourceMetrics) > 0 {
		return otlpFrames(probe.ResourceMetrics, nowMillis)
	}

	if !db.ValidOrgID(probe.OrgID) {
		return nil, errors.New("org_id must be a uuid")
	}
	if !live.ValidStream(probe.Stream) {
		return nil, fmt.Errorf("stream must be one of %v, got %q", live.Streams, probe.Stream)
	}
	ts := probe.TS
	if ts == 0 {
		ts = nowMillis
	}
	points := probe.Points
	if points == nil {
		points = []live.Point{}
	}
	return []orgFrame{{
		OrgID: probe.OrgID,
		Frame: live.Frame{TS: ts, Stream: probe.Stream, Points: points},
	}}, nil
}

// otlpFrames splits an OTLP metrics export into one frame per resource group.
//
// One export can carry several orgs, because the gateway batches across
// tenants. Publishing the whole payload to one org would hand it every other
// tenant in the batch, so the split happens before anything is published.
func otlpFrames(groups []json.RawMessage, nowMillis int64) ([]orgFrame, error) {
	out := make([]orgFrame, 0, len(groups))
	for _, group := range groups {
		orgID, err := orgOfResource(group)
		if err != nil {
			return nil, err
		}
		frame, err := live.FlattenOTLP(group, live.StreamMetric, nowMillis)
		if err != nil {
			return nil, err
		}
		out = append(out, orgFrame{OrgID: orgID, Frame: frame})
	}
	if len(out) == 0 {
		return nil, errors.New("otlp payload carries no resource metrics")
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
