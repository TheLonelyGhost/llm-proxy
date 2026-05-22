// Package telemetry configures process-wide OpenTelemetry tracing.
package telemetry

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	"go.opentelemetry.io/otel/trace"
)

// Config controls application-wide tracing.
type Config struct {
	Enabled  bool
	Exporter string
	Sampler  string
	Ratio    float64
	Service  string
	Writer   io.Writer
}

// NewTracerProvider initializes the global tracer provider.
func NewTracerProvider(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if cfg.Writer == nil {
		cfg.Writer = io.Discard
	}
	if cfg.Service == "" {
		cfg.Service = "llm-proxy"
	}

	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(cfg.Service)),
	)
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	var exporter sdktrace.SpanExporter
	switch strings.ToLower(cfg.Exporter) {
	case "", "stdout":
		exporter, err = stdouttrace.New(stdouttrace.WithWriter(cfg.Writer), stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("init stdout exporter: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported tracing exporter %q", cfg.Exporter)
	}

	var sampler sdktrace.Sampler
	switch strings.ToLower(cfg.Sampler) {
	case "", "always_on":
		sampler = sdktrace.AlwaysSample()
	case "always_off":
		sampler = sdktrace.NeverSample()
	case "ratio":
		if cfg.Ratio < 0 || cfg.Ratio > 1 {
			return nil, fmt.Errorf("tracing ratio must be between 0 and 1, got %v", cfg.Ratio)
		}
		sampler = sdktrace.TraceIDRatioBased(cfg.Ratio)
	case "parent_based_always_on":
		sampler = sdktrace.ParentBased(sdktrace.AlwaysSample())
	case "parent_based_ratio":
		if cfg.Ratio < 0 || cfg.Ratio > 1 {
			return nil, fmt.Errorf("tracing ratio must be between 0 and 1, got %v", cfg.Ratio)
		}
		sampler = sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.Ratio))
	default:
		return nil, fmt.Errorf("unsupported tracing sampler %q", cfg.Sampler)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) trace.Tracer { return otel.Tracer(name) }

// Shutdown flushes the tracer provider.
func Shutdown(ctx context.Context, tp *sdktrace.TracerProvider) error {
	if tp == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown tracer provider: %w", err)
	}
	return nil
}
