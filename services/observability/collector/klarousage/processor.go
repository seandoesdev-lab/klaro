package klarousage

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
	"go.uber.org/zap"
)

// Signal names, matching the CHECK on observability_usage_rollups.
const (
	signalMetrics = "metrics"
	signalTraces  = "traces"
	signalLogs    = "logs"
)

// Sizers report the OTLP protobuf size of a batch. That is the definition of
// "ingested bytes" used for billing: it is what the customer actually sent, it
// is stable across collector versions, and it does not depend on which
// compression a client happened to enable.
var (
	metricsSizer = &pmetric.ProtoMarshaler{}
	tracesSizer  = &ptrace.ProtoMarshaler{}
	logsSizer    = &plog.ProtoMarshaler{}
)

// counter is the processor half: it counts and forwards, never modifies.
type counter struct {
	meter  *meter
	cancel context.CancelFunc
}

// Start begins the flush loop.
//
// The loop lives here rather than on a timer inside the pipeline because a
// pipeline with no traffic still has usage to report - a fleet that went quiet
// is a fact the control plane needs, not an absence of data.
func (c *counter) Start(context.Context, component.Host) error {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.meter.run(ctx)
	return nil
}

// Shutdown flushes what is left, so a redeploy does not discard a partial
// interval of a customer's usage.
func (c *counter) Shutdown(ctx context.Context) error {
	if c.cancel != nil {
		c.cancel()
	}
	c.meter.flush(ctx)
	return nil
}

// hostsOf collects the reporting identities from a resource.
func hostOf(attrs pcommon.Map) host {
	h := host{}
	if v, ok := attrs.Get(attrInstanceID); ok {
		h.Ident = v.AsString()
	}
	if v, ok := attrs.Get(attrServiceName); ok {
		h.Service = v.AsString()
	}
	if v, ok := attrs.Get(attrEnvironment); ok {
		h.Env = v.AsString()
	}
	return h
}

func (c *counter) countMetrics(ctx context.Context, md pmetric.Metrics) (pmetric.Metrics, error) {
	rms := md.ResourceMetrics()
	hosts := make([]host, 0, rms.Len())
	for i := 0; i < rms.Len(); i++ {
		hosts = append(hosts, hostOf(rms.At(i).Resource().Attributes()))
	}
	c.meter.add(orgOf(ctx), signalMetrics, int64(metricsSizer.MetricsSize(md)),
		int64(md.DataPointCount()), hosts)
	return md, nil
}

func (c *counter) countTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	rss := td.ResourceSpans()
	hosts := make([]host, 0, rss.Len())
	for i := 0; i < rss.Len(); i++ {
		hosts = append(hosts, hostOf(rss.At(i).Resource().Attributes()))
	}
	c.meter.add(orgOf(ctx), signalTraces, int64(tracesSizer.TracesSize(td)),
		int64(td.SpanCount()), hosts)
	return td, nil
}

func (c *counter) countLogs(ctx context.Context, ld plog.Logs) (plog.Logs, error) {
	rls := ld.ResourceLogs()
	hosts := make([]host, 0, rls.Len())
	for i := 0; i < rls.Len(); i++ {
		hosts = append(hosts, hostOf(rls.At(i).Resource().Attributes()))
	}
	c.meter.add(orgOf(ctx), signalLogs, int64(logsSizer.LogsSize(ld)),
		int64(ld.LogRecordCount()), hosts)
	return ld, nil
}

var typeStr = component.MustNewType("klarousage")

// NewFactory builds the klarousage processor factory.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		func() component.Config { return &Config{} },
		processor.WithMetrics(createMetrics, component.StabilityLevelAlpha),
		processor.WithTraces(createTraces, component.StabilityLevelAlpha),
		processor.WithLogs(createLogs, component.StabilityLevelAlpha),
	)
}

// observes declares that the processor does not touch the data. Saying so lets
// the collector hand it a shared batch instead of copying one.
var observes = processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: false})

func build(cfg component.Config, logger *zap.Logger) (*counter, error) {
	m, err := newMeter(*cfg.(*Config), logger)
	if err != nil {
		return nil, err
	}
	return &counter{meter: m}, nil
}

func createMetrics(ctx context.Context, set processor.Settings, cfg component.Config,
	next consumer.Metrics) (processor.Metrics, error) {
	c, err := build(cfg, set.Logger)
	if err != nil {
		return nil, err
	}
	return processorhelper.NewMetrics(ctx, set, cfg, next, c.countMetrics, observes,
		processorhelper.WithStart(c.Start), processorhelper.WithShutdown(c.Shutdown))
}

func createTraces(ctx context.Context, set processor.Settings, cfg component.Config,
	next consumer.Traces) (processor.Traces, error) {
	c, err := build(cfg, set.Logger)
	if err != nil {
		return nil, err
	}
	return processorhelper.NewTraces(ctx, set, cfg, next, c.countTraces, observes,
		processorhelper.WithStart(c.Start), processorhelper.WithShutdown(c.Shutdown))
}

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config,
	next consumer.Logs) (processor.Logs, error) {
	c, err := build(cfg, set.Logger)
	if err != nil {
		return nil, err
	}
	return processorhelper.NewLogs(ctx, set, cfg, next, c.countLogs, observes,
		processorhelper.WithStart(c.Start), processorhelper.WithShutdown(c.Shutdown))
}
