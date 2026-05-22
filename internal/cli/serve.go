package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
	"github.com/thelonelyghost/llm-proxy/internal/providers/openai"
	"github.com/thelonelyghost/llm-proxy/internal/server"
	"github.com/thelonelyghost/llm-proxy/internal/telemetry"
	"go.opentelemetry.io/otel/trace"
)

func newServeCmd(out io.Writer) *cobra.Command {
	var (
		cfgPath       string
		address       string
		port          int
		logLevel      string
		traceOn       bool
		traceExporter string
		traceSampler  string
		traceRatio    float64
		traceService  string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the proxy HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			logger := newLogger(out, logLevel)
			slog.SetDefault(logger)

			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			tp, err := telemetry.NewTracerProvider(cmd.Context(), telemetry.Config{
				Enabled:  traceOn || cfg.Server.Tracing,
				Exporter: chooseString(traceExporter, cfg.Server.TraceExporter),
				Sampler:  chooseString(traceSampler, cfg.Server.TraceSampler),
				Ratio:    chooseFloat(traceRatio, cfg.Server.TraceRatio),
				Service:  chooseString(traceService, cfg.Server.TraceService),
				Writer:   out,
			})
			if err != nil {
				return err
			}
			defer func() { _ = telemetry.Shutdown(context.Background(), tp) }()

			logger.Debug("loaded configuration",
				"source", cfg.SourcePath,
				"files", cfg.SourceFiles,
			)

			// CLI flags override file settings when explicitly set.
			if cmd.Flags().Changed("address") {
				cfg.Server.Address = address
			}
			if cmd.Flags().Changed("port") {
				cfg.Server.Port = port
			}

			reg := backend.NewRegistry()
			if err := registerBackends(cfg, reg, telemetry.Tracer("providers")); err != nil {
				return err
			}
			if len(reg.Names()) == 0 {
				return fmt.Errorf("no backends configured")
			}
			logger.Info("backends ready", "backends", reg.Names())

			srv := server.New(server.Options{
				Address:   cfg.Server.Address,
				Port:      cfg.Server.Port,
				Registry:  reg,
				Logger:    logger,
				Tracer:    telemetry.Tracer("server"),
				LogBodies: cfg.Server.LogBodies,
			})

			if cfg.Server.LogBodies {
				logger.Warn("request/response body logging is enabled; treat the log stream as a secret",
					"setting", "server.log_bodies")
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return srv.Run(ctx)
		},
	}
	// default to platform config path if available
	var cfgDefault string
	if p, err := paths.ConfigFile(); err == nil {
		cfgDefault = p
	} else {
		cfgDefault = defaultConfigPath()
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", cfgDefault, "path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	cmd.Flags().StringVar(&address, "address", config.DefaultAddress, "interface address to bind")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "TCP port to bind")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().BoolVar(&traceOn, "trace", false, "enable OpenTelemetry tracing to stdout")
	cmd.Flags().StringVar(&traceExporter, "trace-exporter", "", "tracing exporter to use (stdout)")
	cmd.Flags().StringVar(&traceSampler, "trace-sampler", "", "tracing sampler: always_on, always_off, ratio, parent_based_always_on, parent_based_ratio")
	cmd.Flags().Float64Var(&traceRatio, "trace-ratio", -1, "tracing sample ratio for ratio-based samplers")
	cmd.Flags().StringVar(&traceService, "trace-service", "", "service name attached to trace resource")
	return cmd
}

func registerBackends(cfg *config.Config, reg *backend.Registry, tracer trace.Tracer) error {
	for _, b := range cfg.Backends {
		p, err := buildBackend(b, tracer)
		if err != nil {
			return fmt.Errorf("init backend %q: %w", b.Name, err)
		}
		if err := reg.Register(p); err != nil {
			return fmt.Errorf("register backend %q: %w", b.Name, err)
		}
	}
	return nil
}

// buildBackend instantiates the concrete provider for a BackendBlock. It is
// the single source of truth for the type->constructor mapping so the
// `serve` and `providers list-models` commands stay in sync.
func buildBackend(b config.BackendBlock, tracer trace.Tracer) (backend.Backend, error) {
	switch b.Type {
	case copilot.Type:
		return copilot.New(b, copilot.Options{Name: b.Name, Models: b.Models, Tracer: tracer})
	case ibmbob.Type:
		return ibmbob.New(b, ibmbob.Options{Name: b.Name, Models: b.Models, Tracer: tracer})
	case openai.Type:
		return openai.New(b, openai.Options{Name: b.Name, Models: b.Models, Tracer: tracer})
	default:
		return nil, fmt.Errorf("backend %q: unknown type %q", b.Name, b.Type)
	}
}

func newLogger(w io.Writer, level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl}))
}

func chooseString(cli, cfg string) string {
	if cli != "" {
		return cli
	}
	return cfg
}

func chooseFloat(cli, cfg float64) float64 {
	if cli >= 0 {
		return cli
	}
	return cfg
}
