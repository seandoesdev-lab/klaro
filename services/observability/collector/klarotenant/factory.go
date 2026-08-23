package klarotenant

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"
)

var typeStr = component.MustNewType("klarotenant")

// NewFactory builds the klarotenant processor factory.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		typeStr,
		createDefaultConfig,
		processor.WithMetrics(createMetrics, component.StabilityLevelAlpha),
		processor.WithTraces(createTraces, component.StabilityLevelAlpha),
		processor.WithLogs(createLogs, component.StabilityLevelAlpha),
	)
}

func createDefaultConfig() component.Config { return &Config{} }

// mutates declares that the processor rewrites resource attributes in place,
// which it must, or the collector hands it a shared read-only batch.
var mutates = processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true})

func createMetrics(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Metrics) (processor.Metrics, error) {
	p := stamper{cfg: *cfg.(*Config)}
	return processorhelper.NewMetrics(ctx, set, cfg, next, p.processMetrics, mutates)
}

func createTraces(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Traces) (processor.Traces, error) {
	p := stamper{cfg: *cfg.(*Config)}
	return processorhelper.NewTraces(ctx, set, cfg, next, p.processTraces, mutates)
}

func createLogs(ctx context.Context, set processor.Settings, cfg component.Config, next consumer.Logs) (processor.Logs, error) {
	p := stamper{cfg: *cfg.(*Config)}
	return processorhelper.NewLogs(ctx, set, cfg, next, p.processLogs, mutates)
}
