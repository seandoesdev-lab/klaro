package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/klaro/load-test/internal/model"
)

const jobListKey = "klaro:jobs"

type Redis struct{ c *redis.Client }

func NewRedis(addr string) *Redis {
	return &Redis{c: redis.NewClient(&redis.Options{Addr: addr})}
}

func (r *Redis) Enqueue(ctx context.Context, j model.Job) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return r.c.LPush(ctx, jobListKey, b).Err()
}

func (r *Redis) Dequeue(ctx context.Context) (model.Job, error) {
	// BRPop blocks until a job is available or the 5s timeout elapses.
	res, err := r.c.BRPop(ctx, 5*time.Second, jobListKey).Result()
	if err != nil {
		return model.Job{}, err // includes redis.Nil on timeout
	}
	var j model.Job
	err = json.Unmarshal([]byte(res[1]), &j)
	return j, err
}

func abortChan(id string) string   { return "klaro:abort:" + id }
func metricsChan(id string) string { return "klaro:metrics:" + id }

func (r *Redis) PublishAbort(ctx context.Context, id string) error {
	return r.c.Publish(ctx, abortChan(id), "1").Err()
}

func (r *Redis) SubscribeAbort(ctx context.Context, id string) (<-chan struct{}, func()) {
	sub := r.c.Subscribe(ctx, abortChan(id))
	out := make(chan struct{}, 1)
	go func() {
		for range sub.Channel() {
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out, func() { _ = sub.Close() }
}

func (r *Redis) PublishMetric(ctx context.Context, id string, payload []byte) error {
	return r.c.Publish(ctx, metricsChan(id), payload).Err()
}

func (r *Redis) SubscribeMetrics(ctx context.Context, id string) (<-chan []byte, func()) {
	sub := r.c.Subscribe(ctx, metricsChan(id))
	out := make(chan []byte, 32)
	go func() {
		for msg := range sub.Channel() {
			select {
			case out <- []byte(msg.Payload):
			default: // drop-oldest backpressure: skip if consumer is slow
			}
		}
		close(out)
	}()
	return out, func() { _ = sub.Close() }
}

var _ JobQueue = (*Redis)(nil)
var _ Signaler = (*Redis)(nil)
