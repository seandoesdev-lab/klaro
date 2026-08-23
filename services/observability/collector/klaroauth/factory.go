package klaroauth

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/extension"
)

// typeStr is the name used in the collector configuration.
var typeStr = component.MustNewType("klaroauth")

// NewFactory builds the klaroauth extension factory.
func NewFactory() extension.Factory {
	return extension.NewFactory(typeStr, createDefaultConfig, create, component.StabilityLevelAlpha)
}

func createDefaultConfig() component.Config {
	return &Config{
		Header:           DefaultHeader,
		CacheTTL:         DefaultCacheTTL,
		NegativeCacheTTL: DefaultNegativeCacheTTL,
		Timeout:          DefaultTimeout,
	}
}

func create(_ context.Context, set extension.Settings, cfg component.Config) (extension.Extension, error) {
	return newExtension(*cfg.(*Config), set.Logger)
}
