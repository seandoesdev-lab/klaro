package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/redisx"
)

// Alert is the Alertmanager v2 alert shape vmalert posts.
//
// vmalert speaks the Alertmanager API rather than a bespoke webhook, so the
// control plane accepts that API directly instead of running an Alertmanager in
// between purely to translate. One fewer moving part, same payload.
type Alert struct {
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	StartsAt    time.Time         `json:"startsAt"`
	EndsAt      time.Time         `json:"endsAt"`
}

// Resolved reports whether this alert describes a resolution.
//
// Alertmanager semantics: an EndsAt that is set and not in the future means the
// alert is over. A zero EndsAt means still firing.
func (a Alert) Resolved(now time.Time) bool {
	return !a.EndsAt.IsZero() && !a.EndsAt.After(now)
}

// Receiver turns incoming alerts into event history and notifications.
type Receiver struct {
	store  *Store
	signal redisx.Signaler
	now    func() time.Time
}

// NewReceiver wires a Receiver.
func NewReceiver(store *Store, signal redisx.Signaler) *Receiver {
	return &Receiver{store: store, signal: signal, now: time.Now}
}

// Result summarises what one delivery did, for the response body and the logs.
type Result struct {
	Accepted int `json:"accepted"`
	Opened   int `json:"opened"`
	Resolved int `json:"resolved"`
	Ignored  int `json:"ignored"`
}

// Handle processes a batch of alerts.
//
// The org and rule come from the labels this package put on the rule when it
// rendered it - never from anything else in the payload. Everything else in an
// alert is derived from a metric a customer produced, so treating it as
// authoritative would let a customer's label decide whose alert history a row
// lands in.
func (r *Receiver) Handle(ctx context.Context, alerts []Alert) (Result, error) {
	var res Result
	now := r.now()

	for _, a := range alerts {
		orgID := a.Labels[LabelOrgID]
		ruleID := a.Labels[LabelRuleID]
		if !db.ValidOrgID(orgID) || !db.ValidOrgID(ruleID) {
			// Not one of ours, or malformed. Counting it rather than failing the
			// batch keeps one bad alert from blocking the rest.
			res.Ignored++
			continue
		}

		rule, err := r.store.Get(ctx, orgID, ruleID)
		if errors.Is(err, ErrNotFound) {
			// The rule was deleted between vmalert loading it and firing it.
			res.Ignored++
			continue
		}
		if err != nil {
			return res, err
		}

		labels := publicLabels(a.Labels)
		value := parseValue(a.Annotations["klaro_value"])
		res.Accepted++

		if a.Resolved(now) {
			event, changed, err := r.store.Resolve(ctx, orgID, ruleID, labels)
			if err != nil {
				return res, err
			}
			if changed {
				res.Resolved++
				r.publish(ctx, rule, event, StateResolved, orgID, value, a.Annotations)
			}
			continue
		}

		event, created, err := r.store.OpenFiring(ctx, orgID, ruleID, value, labels, rule.Channels)
		if err != nil {
			return res, err
		}
		if created {
			res.Opened++
			r.publish(ctx, rule, event, StateFiring, orgID, value, a.Annotations)
		}
		// A re-send of an already-open alert is deliberately silent: an operator
		// wants to be told once, not once per evaluation interval.
	}
	return res, nil
}

// publish puts the notification on the shared subject. The Notifier worker
// subscribes and delivers; the receiver does not send mail on the request path,
// so a slow SMTP server cannot stall vmalert.
func (r *Receiver) publish(ctx context.Context, rule Rule, event Event, state, orgID string,
	value *float64, annotations map[string]string) {
	n := Notification{
		Event:        "obs.alert",
		OrgID:        orgID,
		RuleID:       rule.ID,
		RuleName:     rule.Name,
		Signal:       rule.Signal,
		Value:        value,
		Threshold:    rule.Threshold,
		State:        state,
		At:           event.StartedAt,
		DashboardURL: annotations["klaro_dashboard_url"],
		Channels:     rule.Channels,
	}
	if state == StateResolved && event.ResolvedAt != nil {
		n.At = *event.ResolvedAt
	}
	payload, err := json.Marshal(n)
	if err != nil {
		log.Printf("alerting: encode notification: %v", err)
		return
	}
	if err := r.signal.Publish(ctx, AlertSubject, payload); err != nil {
		// The event is already recorded, so the history is right even when the
		// notification is lost. Losing the history would be the bad outcome.
		log.Printf("alerting: publish notification: %v", err)
	}
}

// publicLabels drops the labels this package added for routing. They identify
// the tenant, and an alert history row that repeated them would invite a client
// to read tenancy out of its own data.
func publicLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		switch k {
		case LabelOrgID, LabelRuleID, LabelVMAccount:
			continue
		}
		out[k] = v
	}
	return out
}

func parseValue(raw string) *float64 {
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil
	}
	return &v
}

// RunNotifier subscribes to AlertSubject and delivers each notification.
//
// A subscriber rather than a direct call, per design section 4.5: the receiver
// is on vmalert's request path and must return quickly, while delivery talks to
// SMTP and HTTP and can be slow.
func RunNotifier(ctx context.Context, signal redisx.Signaler, router Router) {
	messages, cancel, err := signal.Subscribe(ctx, AlertSubject)
	if err != nil {
		log.Printf("alerting: notifier could not subscribe: %v", err)
		return
	}
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return
		case raw, ok := <-messages:
			if !ok {
				return
			}
			var n Notification
			if err := json.Unmarshal(raw, &n); err != nil {
				log.Printf("alerting: notifier got undecodable payload: %v", err)
				continue
			}
			delivered := router.Notify(ctx, n)
			log.Printf("alerting: notified org=%s rule=%s state=%s channels=%d/%d",
				n.OrgID, n.RuleName, n.State, len(delivered), len(n.Channels))
		}
	}
}
