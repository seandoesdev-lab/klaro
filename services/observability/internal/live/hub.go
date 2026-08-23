package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/klaro/observability/internal/platform/db"
	"github.com/klaro/observability/internal/platform/redisx"
)

// ErrInvalidScope is returned for an org or stream the hub will not subscribe to.
var ErrInvalidScope = errors.New("invalid live scope")

// subscriberBuffer is how many frames a slow client may fall behind before the
// hub starts dropping for it. Mirrors the Signaler bound; a live view that
// skips a frame is fine, a hub that blocks the publisher is not.
const subscriberBuffer = 32

// Hub multiplexes one Redis subscription per org+stream out to every local
// WebSocket watching it.
//
// The naive alternative - one Redis subscription per socket, as S1 does per
// load test - is fine when a channel has one viewer, and wrong here: an org
// with a dashboard open on twenty screens would hold twenty subscriptions to
// the same channel and receive every frame twenty times. The hub collapses
// that to one, and reference counts so the upstream subscription closes when
// the last viewer leaves.
type Hub struct {
	signal redisx.Signaler

	mu     sync.Mutex
	fanout map[string]*fanout
}

// NewHub builds a hub over a Signaler.
func NewHub(signal redisx.Signaler) *Hub {
	return &Hub{signal: signal, fanout: map[string]*fanout{}}
}

// fanout is the shared state for one org+stream channel.
type fanout struct {
	cancelUpstream func()

	mu          sync.Mutex
	subscribers map[chan []byte]struct{}
}

// Subscribe returns a channel of frames for org+stream and a cancel func.
//
// The returned channel is closed when the caller cancels or the hub shuts the
// upstream subscription. Cancel is safe to call more than once.
func (h *Hub) Subscribe(ctx context.Context, orgID, stream string) (<-chan []byte, func(), error) {
	if !db.ValidOrgID(orgID) {
		return nil, nil, fmt.Errorf("%w: org %q is not a uuid", ErrInvalidScope, orgID)
	}
	if !ValidStream(stream) {
		return nil, nil, fmt.Errorf("%w: unknown stream %q", ErrInvalidScope, stream)
	}

	key := redisx.LiveChannel(orgID, stream)
	ch := make(chan []byte, subscriberBuffer)

	h.mu.Lock()
	f, ok := h.fanout[key]
	if !ok {
		up, cancelUp, err := h.signal.SubscribeLive(ctx, orgID, stream)
		if err != nil {
			h.mu.Unlock()
			return nil, nil, fmt.Errorf("subscribe %s: %w", key, err)
		}
		f = &fanout{cancelUpstream: cancelUp, subscribers: map[chan []byte]struct{}{}}
		h.fanout[key] = f
		go h.pump(key, f, up)
	}
	f.mu.Lock()
	f.subscribers[ch] = struct{}{}
	f.mu.Unlock()
	h.mu.Unlock()

	var once sync.Once
	return ch, func() { once.Do(func() { h.remove(key, f, ch) }) }, nil
}

// pump copies upstream frames to every local subscriber until the upstream
// channel closes.
func (h *Hub) pump(key string, f *fanout, up <-chan []byte) {
	for msg := range up {
		f.deliver(msg)
	}
	// Upstream ended (Redis closed, or the subscribing context was cancelled).
	// Close every local subscriber so their sockets shut down rather than hang.
	h.mu.Lock()
	if h.fanout[key] == f {
		delete(h.fanout, key)
	}
	h.mu.Unlock()

	f.mu.Lock()
	for ch := range f.subscribers {
		delete(f.subscribers, ch)
		close(ch)
	}
	f.mu.Unlock()
}

// deliver hands one frame to every subscriber, never blocking on any of them.
//
// Drop-oldest, not drop-newest: a live view that has fallen behind should show
// what is happening now, not replay what it missed. Dropping the newest frame
// would leave the slowest tab permanently stale.
func (f *fanout) deliver(msg []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subscribers {
		select {
		case ch <- msg:
			continue
		default:
		}
		select {
		case <-ch: // discard the oldest frame to make room
		default:
		}
		select {
		case ch <- msg:
		default:
			// The reader emptied and refilled it between our two operations.
			// Skipping this frame is the whole point of the bound.
		}
	}
}

// remove detaches one subscriber, tearing down the upstream subscription when
// it was the last one.
func (h *Hub) remove(key string, f *fanout, ch chan []byte) {
	h.mu.Lock()
	f.mu.Lock()
	if _, still := f.subscribers[ch]; still {
		delete(f.subscribers, ch)
		close(ch)
	}
	last := len(f.subscribers) == 0
	f.mu.Unlock()
	if last && h.fanout[key] == f {
		delete(h.fanout, key)
	}
	h.mu.Unlock()

	if last {
		f.cancelUpstream()
	}
}

// Channels reports how many org+stream subscriptions the hub currently holds.
// Used by the tests to prove the reference counting actually releases them.
func (h *Hub) Channels() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.fanout)
}

// Publish encodes a frame onto the org+stream channel. The control plane uses
// it from the Collector ingest path.
func Publish(ctx context.Context, signal redisx.Signaler, orgID string, f Frame) error {
	if !db.ValidOrgID(orgID) {
		return fmt.Errorf("%w: org %q is not a uuid", ErrInvalidScope, orgID)
	}
	if !ValidStream(f.Stream) {
		return fmt.Errorf("%w: unknown stream %q", ErrInvalidScope, f.Stream)
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode live frame: %w", err)
	}
	return signal.PublishLive(ctx, orgID, f.Stream, payload)
}
