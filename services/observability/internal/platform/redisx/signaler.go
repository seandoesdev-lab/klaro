// Package redisx carries live telemetry from the Collector ingest endpoint to
// the WebSocket hub.
//
// This is S1's Signaler (services/load-test/internal/queue/redis.go) rescoped
// from load_test_id to org+stream, per design HOW-7. Same drop-oldest
// backpressure: a live view that skips a frame is fine, a hub that blocks the
// publisher is not.
package redisx

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// ErrInvalidScope is returned for an empty org or stream.
var ErrInvalidScope = errors.New("invalid live scope")

// Signaler publishes and subscribes org-scoped live telemetry.
type Signaler interface {
	// PublishLive fans a payload out to every subscriber of org+stream.
	PublishLive(ctx context.Context, orgID, stream string, payload []byte) error
	// SubscribeLive returns a receive channel and a cancel func. The channel is
	// closed when the subscription ends.
	SubscribeLive(ctx context.Context, orgID, stream string) (<-chan []byte, func(), error)
	// Publish sends a payload on a plain, non-org-scoped subject: alerting and
	// usage events are read by internal services, not by tenants.
	Publish(ctx context.Context, subject string, payload []byte) error
	// Subscribe receives a plain subject.
	Subscribe(ctx context.Context, subject string) (<-chan []byte, func(), error)
	Close() error
}

// subscriberBuffer bounds how far a slow WS client may lag before frames are
// dropped for it.
const subscriberBuffer = 32

// LiveChannel is the Redis pub/sub channel for one org+stream pair.
//
// The org id is part of the channel name, so a subscriber can only ever receive
// the tenant it asked for - there is no shared channel to filter after the fact.
func LiveChannel(orgID, stream string) string {
	return fmt.Sprintf("klaro:obs:live:%s:%s", orgID, stream)
}

// Redis is the production Signaler.
type Redis struct{ c *redis.Client }

// NewRedis dials addr lazily (go-redis connects on first use).
func NewRedis(addr string) *Redis {
	return &Redis{c: redis.NewClient(&redis.Options{Addr: addr})}
}

func validScope(orgID, stream string) error {
	if orgID == "" || stream == "" {
		return fmt.Errorf("%w: org=%q stream=%q", ErrInvalidScope, orgID, stream)
	}
	return nil
}

// PublishLive implements Signaler.
func (r *Redis) PublishLive(ctx context.Context, orgID, stream string, payload []byte) error {
	if err := validScope(orgID, stream); err != nil {
		return err
	}
	return r.c.Publish(ctx, LiveChannel(orgID, stream), payload).Err()
}

// SubscribeLive implements Signaler.
func (r *Redis) SubscribeLive(ctx context.Context, orgID, stream string) (<-chan []byte, func(), error) {
	if err := validScope(orgID, stream); err != nil {
		return nil, nil, err
	}
	sub := r.c.Subscribe(ctx, LiveChannel(orgID, stream))
	out := make(chan []byte, subscriberBuffer)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			select {
			case out <- []byte(msg.Payload):
			default: // drop-oldest backpressure: never block the publisher
			}
		}
	}()
	return out, func() { _ = sub.Close() }, nil
}

// Close releases the client.
func (r *Redis) Close() error { return r.c.Close() }

// Memory is an in-process Signaler for tests and single-node dev runs.
type Memory struct {
	mu   sync.Mutex
	subs map[string][]chan []byte
}

// NewMemory builds an empty in-process Signaler.
func NewMemory() *Memory { return &Memory{subs: map[string][]chan []byte{}} }

// PublishLive implements Signaler.
func (m *Memory) PublishLive(_ context.Context, orgID, stream string, payload []byte) error {
	if err := validScope(orgID, stream); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs[LiveChannel(orgID, stream)] {
		select {
		case ch <- payload:
		default:
		}
	}
	return nil
}

// SubscribeLive implements Signaler.
func (m *Memory) SubscribeLive(_ context.Context, orgID, stream string) (<-chan []byte, func(), error) {
	if err := validScope(orgID, stream); err != nil {
		return nil, nil, err
	}
	key := LiveChannel(orgID, stream)
	ch := make(chan []byte, subscriberBuffer)
	m.mu.Lock()
	m.subs[key] = append(m.subs[key], ch)
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			kept := m.subs[key][:0]
			for _, c := range m.subs[key] {
				if c != ch {
					kept = append(kept, c)
				}
			}
			m.subs[key] = kept
			close(ch)
		})
	}
	return ch, cancel, nil
}

// Close implements Signaler.
func (m *Memory) Close() error { return nil }

var (
	_ Signaler = (*Redis)(nil)
	_ Signaler = (*Memory)(nil)
)

// Publish sends a payload on a plain subject.
//
// Separate from PublishLive because the audience is different: a live channel is
// org-scoped and read by that tenant's browsers, while a subject like
// klaro.obs.alert or klaro.usage.emitted is read by internal services
// (Notifier, Billing) that legitimately see every org. Keeping them apart means
// the org-scoped path cannot accidentally be handed a global subject.
func (r *Redis) Publish(ctx context.Context, subject string, payload []byte) error {
	if subject == "" {
		return fmt.Errorf("%w: empty subject", ErrInvalidScope)
	}
	return r.c.Publish(ctx, subject, payload).Err()
}

// Subscribe receives a plain subject.
func (r *Redis) Subscribe(ctx context.Context, subject string) (<-chan []byte, func(), error) {
	if subject == "" {
		return nil, nil, fmt.Errorf("%w: empty subject", ErrInvalidScope)
	}
	sub := r.c.Subscribe(ctx, subject)
	out := make(chan []byte, subscriberBuffer)
	go func() {
		defer close(out)
		for msg := range sub.Channel() {
			select {
			case out <- []byte(msg.Payload):
			default: // drop-oldest backpressure: never block the publisher
			}
		}
	}()
	return out, func() { _ = sub.Close() }, nil
}

// Publish implements Signaler for the in-process fake.
func (m *Memory) Publish(_ context.Context, subject string, payload []byte) error {
	if subject == "" {
		return fmt.Errorf("%w: empty subject", ErrInvalidScope)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs[subject] {
		select {
		case ch <- payload:
		default:
		}
	}
	return nil
}

// Subscribe implements Signaler for the in-process fake.
func (m *Memory) Subscribe(_ context.Context, subject string) (<-chan []byte, func(), error) {
	if subject == "" {
		return nil, nil, fmt.Errorf("%w: empty subject", ErrInvalidScope)
	}
	ch := make(chan []byte, subscriberBuffer)
	m.mu.Lock()
	m.subs[subject] = append(m.subs[subject], ch)
	m.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			kept := m.subs[subject][:0]
			for _, c := range m.subs[subject] {
				if c != ch {
					kept = append(kept, c)
				}
			}
			m.subs[subject] = kept
			close(ch)
		})
	}
	return ch, cancel, nil
}
