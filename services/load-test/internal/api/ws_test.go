package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeSignal implements queue.Signaler for streaming tests.
type fakeSignal struct{ ch chan []byte }

func (f *fakeSignal) PublishAbort(context.Context, string) error { return nil }
func (f *fakeSignal) SubscribeAbort(context.Context, string) (<-chan struct{}, func()) {
	return make(chan struct{}), func() {}
}
func (f *fakeSignal) PublishMetric(_ context.Context, _ string, p []byte) error {
	f.ch <- p
	return nil
}
func (f *fakeSignal) SubscribeMetrics(context.Context, string) (<-chan []byte, func()) {
	return f.ch, func() {}
}

func TestWSStream(t *testing.T) {
	fs := &fakeSignal{ch: make(chan []byte, 4)}
	r := NewRouter(Deps{Signal: fs, DevToken: "dev"})
	srv := httptest.NewServer(r)
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/load-tests/abc/stream"
	c, _, err := websocket.DefaultDialer.Dial(url, http.Header{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	fs.ch <- []byte(`{"rps":10}`)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "rps") {
		t.Fatalf("unexpected msg %s", msg)
	}
}
