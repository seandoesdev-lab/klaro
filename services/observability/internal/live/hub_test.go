package live

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/klaro/observability/internal/platform/redisx"
)

const (
	orgA = "00000000-0000-0000-0000-00000000000a"
	orgB = "00000000-0000-0000-0000-00000000000b"
)

func publish(t *testing.T, signal redisx.Signaler, org string, value float64) {
	t.Helper()
	err := Publish(context.Background(), signal, org, Frame{
		TS: 1, Stream: StreamMetric,
		Points: []Point{{Labels: map[string]string{"__name__": "cpu"}, Value: value}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// waitFrame blocks briefly for one frame: the pump copies on its own goroutine,
// so a non-blocking read would be a race rather than an assertion.
func waitFrame(t *testing.T, ch <-chan []byte) Frame {
	t.Helper()
	select {
	case raw, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before a frame arrived")
		}
		var f Frame
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame arrived")
		return Frame{}
	}
}

// Many viewers of one org must share a single upstream subscription. Otherwise
// a dashboard open on twenty screens holds twenty Redis subscriptions to the
// same channel and receives every frame twenty times.
func TestHubSharesOneUpstreamPerChannel(t *testing.T) {
	h := NewHub(redisx.NewMemory())
	ctx := context.Background()

	a, cancelA, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	b, cancelB, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()

	if got := h.Channels(); got != 1 {
		t.Errorf("upstream subscriptions = %d, want 1", got)
	}
	_ = a
	_ = b
}

func TestHubFansOutToEverySubscriber(t *testing.T) {
	signal := redisx.NewMemory()
	h := NewHub(signal)
	ctx := context.Background()

	a, cancelA, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()
	b, cancelB, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelB()

	publish(t, signal, orgA, 0.5)
	for name, ch := range map[string]<-chan []byte{"a": a, "b": b} {
		f := waitFrame(t, ch)
		if len(f.Points) != 1 || f.Points[0].Value != 0.5 {
			t.Errorf("%s received %+v", name, f)
		}
	}
}

// The channel name carries the org, so isolation is structural: a subscriber
// cannot be handed another tenant's frames even by mistake.
func TestHubDoesNotCrossOrgs(t *testing.T) {
	signal := redisx.NewMemory()
	h := NewHub(signal)
	ctx := context.Background()

	watcher, cancel, err := h.Subscribe(ctx, orgB, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	publish(t, signal, orgA, 1)
	select {
	case got := <-watcher:
		t.Fatalf("orgB received orgA data: %s", got)
	default:
	}
}

// Streams are separate channels: a metric watcher must not receive service
// frames.
func TestHubSeparatesStreams(t *testing.T) {
	signal := redisx.NewMemory()
	h := NewHub(signal)
	ctx := context.Background()

	metric, cancelM, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelM()
	service, cancelS, err := h.Subscribe(ctx, orgA, StreamService)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelS()

	if got := h.Channels(); got != 2 {
		t.Errorf("channels = %d, want 2", got)
	}
	publish(t, signal, orgA, 3)
	if f := waitFrame(t, metric); len(f.Points) != 1 {
		t.Errorf("metric watcher got %+v", f)
	}
	select {
	case got := <-service:
		t.Fatalf("service watcher received a metric frame: %s", got)
	default:
	}
}

// The upstream subscription must be released when the last viewer leaves, or
// the hub leaks a Redis subscription per abandoned dashboard.
func TestHubReleasesUpstreamWhenLastSubscriberLeaves(t *testing.T) {
	h := NewHub(redisx.NewMemory())
	ctx := context.Background()

	_, cancelA, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	_, cancelB, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}

	cancelA()
	if got := h.Channels(); got != 1 {
		t.Fatalf("channels after one leaver = %d, want 1", got)
	}
	cancelB()
	if got := h.Channels(); got != 0 {
		t.Errorf("channels after the last leaver = %d, want 0", got)
	}
	// Cancelling twice must not panic on a closed channel.
	cancelB()
}

// A stalled browser tab must not stall the fan-out for everyone else, so the
// slowest subscriber loses its oldest frames rather than blocking.
//
// This drives fanout.deliver directly instead of publishing through Redis: the
// pump runs on its own goroutine, so going through the signaler would be a race
// against the pump rather than a test of the bound.
func TestHubDropsOldestForASlowSubscriber(t *testing.T) {
	h := NewHub(redisx.NewMemory())
	ctx := context.Background()

	slow, cancelSlow, err := h.Subscribe(ctx, orgA, StreamMetric)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelSlow()

	h.mu.Lock()
	f := h.fanout[redisx.LiveChannel(orgA, StreamMetric)]
	h.mu.Unlock()
	if f == nil {
		t.Fatal("no fanout was registered")
	}

	total := subscriberBuffer * 3
	for i := 0; i < total; i++ {
		f.deliver([]byte(strconv.Itoa(i)))
	}

	var got []int
	for {
		select {
		case raw := <-slow:
			n, err := strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, n)
			continue
		default:
		}
		break
	}

	if len(got) != subscriberBuffer {
		t.Fatalf("buffered %d frames, want the bound of %d", len(got), subscriberBuffer)
	}
	// Dropping the oldest means the newest survives: a live view that has
	// fallen behind should show what is happening now, not replay what it
	// missed.
	if got[len(got)-1] != total-1 {
		t.Errorf("newest retained frame = %d, want %d", got[len(got)-1], total-1)
	}
	if got[0] != total-subscriberBuffer {
		t.Errorf("oldest retained frame = %d, want %d", got[0], total-subscriberBuffer)
	}
}

func TestHubRejectsBadScope(t *testing.T) {
	h := NewHub(redisx.NewMemory())
	ctx := context.Background()

	if _, _, err := h.Subscribe(ctx, "not-a-uuid", StreamMetric); err == nil {
		t.Error("a malformed org was accepted")
	}
	if _, _, err := h.Subscribe(ctx, orgA, "metric:other"); err == nil {
		t.Error("a stream that escapes its channel was accepted")
	}
	if h.Channels() != 0 {
		t.Error("a rejected subscribe still opened a channel")
	}
}

func TestPublishRejectsBadScope(t *testing.T) {
	signal := redisx.NewMemory()
	if err := Publish(context.Background(), signal, "nope", Frame{Stream: StreamMetric}); err == nil {
		t.Error("a malformed org was accepted")
	}
	if err := Publish(context.Background(), signal, orgA, Frame{Stream: "bogus"}); err == nil {
		t.Error("an unknown stream was accepted")
	}
}
