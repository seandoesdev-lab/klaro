package redisx

import (
	"context"
	"errors"
	"testing"
	"time"
)

const (
	orgA = "00000000-0000-0000-0000-00000000000a"
	orgB = "00000000-0000-0000-0000-00000000000b"
)

func TestLiveChannelIsOrgScoped(t *testing.T) {
	if got, want := LiveChannel(orgA, "metric"), "klaro:obs:live:"+orgA+":metric"; got != want {
		t.Errorf("LiveChannel = %q, want %q", got, want)
	}
	if LiveChannel(orgA, "metric") == LiveChannel(orgB, "metric") {
		t.Error("different orgs must map to different channels")
	}
	if LiveChannel(orgA, "metric") == LiveChannel(orgA, "service") {
		t.Error("different streams must map to different channels")
	}
}

func recv(t *testing.T, ch <-chan []byte) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a live frame")
		return nil
	}
}

func TestMemoryPublishReachesSubscriber(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	ch, cancel, err := m.SubscribeLive(ctx, orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	if err := m.PublishLive(ctx, orgA, "metric", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := string(recv(t, ch)); got != `{"v":1}` {
		t.Errorf("payload = %q", got)
	}
}

// A subscriber must never observe another org's telemetry, even on the same
// stream name - the org is baked into the channel key.
func TestMemoryDoesNotLeakAcrossOrgs(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	chA, cancelA, err := m.SubscribeLive(ctx, orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancelA()

	if err := m.PublishLive(ctx, orgB, "metric", []byte("other-tenant")); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-chA:
		t.Fatalf("org A received org B telemetry: %q", b)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMemoryDropsWhenSubscriberIsFull(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	ch, cancel, err := m.SubscribeLive(ctx, orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	// Publishing far past the buffer must not block or error.
	done := make(chan struct{})
	go func() {
		for i := 0; i < subscriberBuffer*4; i++ {
			if err := m.PublishLive(ctx, orgA, "metric", []byte("x")); err != nil {
				t.Errorf("publish: %v", err)
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked on a full subscriber; backpressure is not drop-oldest")
	}
	if len(ch) != subscriberBuffer {
		t.Errorf("buffered = %d, want %d", len(ch), subscriberBuffer)
	}
}

func TestMemoryCancelIsIdempotent(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	ch, cancel, err := m.SubscribeLive(ctx, orgA, "metric")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cancel() // must not panic on a double close
	if _, open := <-ch; open {
		t.Error("channel should be closed after cancel")
	}
	// Publishing to a cancelled subscription must not panic either.
	if err := m.PublishLive(ctx, orgA, "metric", []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidScopeRejected(t *testing.T) {
	ctx := context.Background()
	for _, s := range []Signaler{NewMemory(), NewRedis("127.0.0.1:1")} {
		if err := s.PublishLive(ctx, "", "metric", nil); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("%T publish empty org: %v", s, err)
		}
		if err := s.PublishLive(ctx, orgA, "", nil); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("%T publish empty stream: %v", s, err)
		}
		if _, _, err := s.SubscribeLive(ctx, "", "metric"); !errors.Is(err, ErrInvalidScope) {
			t.Errorf("%T subscribe empty org: %v", s, err)
		}
	}
}
