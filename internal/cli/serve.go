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
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
	"github.com/thelonelyghost/llm-proxy/internal/server"
)

func newServeCmd(out io.Writer) *cobra.Command {
	var (
		cfgPath  string
		address  string
		port     int
		logLevel string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the proxy HTTP server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			// CLI flags override file settings when explicitly set.
			if cmd.Flags().Changed("address") {
				cfg.Server.Address = address
			}
			if cmd.Flags().Changed("port") {
				cfg.Server.Port = port
			}

			logger := newLogger(out, logLevel)
			reg := backend.NewRegistry()
			if err := registerBackends(cfg, reg); err != nil {
				return err
			}
			if len(reg.Names()) == 0 {
				return fmt.Errorf("no backends configured")
			}
			logger.Info("backends ready", "backends", reg.Names())

			srv := server.New(server.Options{
				Address:  cfg.Server.Address,
				Port:     cfg.Server.Port,
				Registry: reg,
				Logger:   logger,
			})

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return srv.Run(ctx)
		},
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", "", "path to HCL config file or directory (required)")
	cmd.Flags().StringVar(&address, "address", config.DefaultAddress, "interface address to bind")
	cmd.Flags().IntVar(&port, "port", config.DefaultPort, "TCP port to bind")
	cmd.Flags().StringVar(&logLevel, "log-level", "info", "log level: debug, info, warn, error")
	_ = cmd.MarkFlagRequired("config")
	return cmd
}

func registerBackends(cfg *config.Config, reg *backend.Registry) error {
	for _, b := range cfg.Backends {
		p, err := buildBackend(b)
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
func buildBackend(b config.BackendBlock) (backend.Backend, error) {
	switch b.Type {
	case copilot.Type:
		return copilot.New(b, copilot.Options{Name: b.Name, Models: b.Models})
	case ibmbob.Type:
		return ibmbob.New(b, ibmbob.Options{Name: b.Name, Models: b.Models})
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

// Ensure context import is used even if go-vet flags it.
var _ = context.Background
