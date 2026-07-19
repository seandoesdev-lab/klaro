package queue

import (
	"context"

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

type Signaler interface {
	PublishAbort(ctx context.Context, loadTestID string) error
	SubscribeAbort(ctx context.Context, loadTestID string) (<-chan struct{}, func())
	PublishMetric(ctx context.Context, loadTestID string, payload []byte) error
	SubscribeMetrics(ctx context.Context, loadTestID string) (<-chan []byte, func())
}
