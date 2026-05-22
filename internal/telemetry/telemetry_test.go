package telemetry_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/telemetry"
)

func TestNewTracerProvider_Stdout(t *testing.T) {
	var buf bytes.Buffer
	tp, err := telemetry.NewTracerProvider(context.Background(), telemetry.Config{
		Enabled:  true,
		Exporter: "stdout",
		Sampler:  "always_on",
		Writer:   &buf,
		Service:  "test-service",
	})
	if err != nil {
		t.Fatalf("NewTracerProvider: %v", err)
	}
	if tp == nil {
		t.Fatal("expected tracer provider")
	}
	tracer := telemetry.Tracer("test")
	_, span := tracer.Start(context.Background(), "unit-test")
	span.End()
	if err := telemetry.Shutdown(context.Background(), tp); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !strings.Contains(buf.String(), "test-service") {
		t.Fatalf("stdout export missing service name: %s", buf.String())
	}
}

func TestNewTracerProvider_Disabled(t *testing.T) {
	tp, err := telemetry.NewTracerProvider(context.Background(), telemetry.Config{Enabled: false})
	if err != nil {
		t.Fatalf("NewTracerProvider: %v", err)
	}
	if tp != nil {
		t.Fatalf("expected nil tracer provider, got %v", tp)
	}
	if err := telemetry.Shutdown(context.Background(), nil); err != nil {
		t.Fatalf("Shutdown nil: %v", err)
	}
}

func TestNewTracerProvider_Errors(t *testing.T) {
	tests := []struct {
		name string
		cfg  telemetry.Config
	}{
		{name: "bad exporter", cfg: telemetry.Config{Enabled: true, Exporter: "file"}},
		{name: "bad sampler", cfg: telemetry.Config{Enabled: true, Sampler: "weird"}},
		{name: "bad ratio", cfg: telemetry.Config{Enabled: true, Sampler: "ratio", Ratio: 1.5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := telemetry.NewTracerProvider(context.Background(), tt.cfg); err == nil {
				t.Fatalf("expected error for %+v", tt.cfg)
			}
		})
	}
}
