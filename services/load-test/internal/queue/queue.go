package queue

import (
	"context"
	"time"

	"github.com/klaro/load-test/internal/model"
)

type JobQueue interface {
	Enqueue(ctx context.Context, j model.Job) error
	Dequeue(ctx context.Context) (model.Job, error)
}

// ScanQueue carries security-scan jobs on a separate Redis list from load tests.
type ScanQueue interface {
	EnqueueScan(ctx context.Context, j model.ScanJob) error
	DequeueScan(ctx context.Context) (model.ScanJob, error)
}

// SrcTokenStore binds a SAST upload token to the org that uploaded it, with a TTL
// ([M-2] tenant isolation of staged source archives).
type SrcTokenStore interface {
	PutSrcToken(ctx context.Context, token, orgID string, ttl time.Duration) error
	GetSrcToken(ctx context.Context, token string) (string, error)
}

type Signaler interface {
	PublishAbort(ctx context.Context, loadTestID string) error
	SubscribeAbort(ctx context.Context, loadTestID string) (<-chan struct{}, func())
	PublishMetric(ctx context.Context, loadTestID string, payload []byte) error
	SubscribeMetrics(ctx context.Context, loadTestID string) (<-chan []byte, func())
}
