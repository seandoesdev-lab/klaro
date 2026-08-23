package alerting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// AlertSubject is the pub/sub subject alert notifications travel on
// (design section 4.5). S1's circuit-breaker notifications use klaro.notify;
// keeping the subjects apart means a consumer can take one without the other.
const AlertSubject = "klaro.obs.alert"

// Notification is the payload published on AlertSubject. The field set is the
// minimum schema from design HOW-1 - anything a channel template might want has
// to be here, because the consumer has no database.
type Notification struct {
	Event        string    `json:"event"`
	OrgID        string    `json:"org_id"`
	RuleID       string    `json:"rule_id"`
	RuleName     string    `json:"rule_name"`
	Signal       string    `json:"signal"`
	Value        *float64  `json:"value"`
	Threshold    float64   `json:"threshold"`
	State        string    `json:"state"`
	At           time.Time `json:"at"`
	DashboardURL string    `json:"dashboard_url,omitempty"`
	Channels     []Channel `json:"channels"`
}

// Notifier delivers one notification to one destination.
//
// The interface is shared with S1's notifier shape on purpose: a deployment
// runs one set of channel credentials, and two divergent notion of "send this
// to a customer" would mean two places to get an outage announcement wrong.
type Notifier interface {
	Notify(ctx context.Context, n Notification, ch Channel) error
}

// LogNotifier records what would have been sent. It is the default for a
// channel type with no transport configured - a development deployment should
// still be able to see that alerting works end to end.
type LogNotifier struct{}

// Notify implements Notifier.
func (LogNotifier) Notify(_ context.Context, n Notification, ch Channel) error {
	log.Printf("alert notify (no transport configured) type=%s target=%s org=%s rule=%s state=%s value=%v threshold=%v",
		ch.Type, ch.Target, n.OrgID, n.RuleName, n.State, valueOf(n.Value), n.Threshold)
	return nil
}

// SMTPNotifier sends mail. In development this points at MailHog, which accepts
// anything and shows it in a web UI, so no real mail is ever sent by accident.
type SMTPNotifier struct {
	Addr string // host:port, e.g. mailhog:1025
	From string
}

// Notify implements Notifier.
func (s SMTPNotifier) Notify(_ context.Context, n Notification, ch Channel) error {
	if s.Addr == "" {
		return fmt.Errorf("smtp notifier has no address")
	}
	subject := fmt.Sprintf("[klaro %s] %s", strings.ToUpper(n.State), n.RuleName)
	body := fmt.Sprintf(
		"rule:      %s\norg:       %s\nsignal:    %s\nstate:     %s\nvalue:     %v\nthreshold: %v\nat:        %s\n%s\n",
		n.RuleName, n.OrgID, n.Signal, n.State, valueOf(n.Value), n.Threshold,
		n.At.UTC().Format(time.RFC3339), n.DashboardURL)

	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s", s.From, ch.Target, subject, body)
	// No auth: MailHog wants none, and a production SMTP relay is a separate
	// decision that should be made with credentials in hand.
	return smtp.SendMail(s.Addr, nil, s.From, []string{ch.Target}, []byte(msg))
}

// SlackNotifier posts to an incoming webhook. The channel target is the message
// prefix, not the URL: a customer-supplied URL would make this an
// outbound-request primitive pointed wherever they liked.
type SlackNotifier struct {
	WebhookURL string
	Client     *http.Client
}

// Notify implements Notifier.
func (s SlackNotifier) Notify(ctx context.Context, n Notification, ch Channel) error {
	if s.WebhookURL == "" {
		return fmt.Errorf("slack notifier has no webhook url")
	}
	text := fmt.Sprintf("*%s* %s — %s (value %v, threshold %v) %s",
		strings.ToUpper(n.State), ch.Target, n.RuleName, valueOf(n.Value), n.Threshold, n.DashboardURL)
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook returned %s", resp.Status)
	}
	return nil
}

// Router picks a Notifier per channel type and falls back to logging.
//
// Falling back rather than failing is deliberate: an unconfigured Slack webhook
// must not stop the email for the same alert from going out.
type Router struct {
	Email    Notifier
	Slack    Notifier
	Fallback Notifier
}

// NewRouter wires the transports a deployment actually has.
func NewRouter(email, slack Notifier) Router {
	return Router{Email: email, Slack: slack, Fallback: LogNotifier{}}
}

// Notify delivers to every channel on the notification, reporting how many
// succeeded. One failing channel does not abort the others.
func (r Router) Notify(ctx context.Context, n Notification) []Channel {
	delivered := []Channel{}
	for _, ch := range n.Channels {
		var target Notifier
		switch ch.Type {
		case ChannelEmail:
			target = r.Email
		case ChannelSlack:
			target = r.Slack
		}
		if target == nil {
			target = r.Fallback
		}
		if target == nil {
			continue
		}
		if err := target.Notify(ctx, n, ch); err != nil {
			log.Printf("alert notify failed type=%s org=%s rule=%s: %v", ch.Type, n.OrgID, n.RuleName, err)
			continue
		}
		delivered = append(delivered, ch)
	}
	return delivered
}

func valueOf(v *float64) any {
	if v == nil {
		return "n/a"
	}
	return *v
}
