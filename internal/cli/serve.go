package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/backend/sidecar"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/lockfile"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
	"github.com/thelonelyghost/llm-proxy/internal/pluginrunner"
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

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			reg := backend.NewRegistry()
			if err := registerBackends(ctx, cfg, reg, logger, telemetry.Tracer("providers")); err != nil {
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

			// SIGHUP: reload on-disk SSO credentials for any backend that
			// implements the Reloadable extension, without restarting the
			// HTTP listener.
			sighupCh := make(chan os.Signal, 1)
			signal.Notify(sighupCh, syscall.SIGHUP)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-sighupCh:
						logger.Info("SIGHUP received: reloading SSO credential caches")
						for _, name := range reg.Names() {
							if b, ok := reg.Get(name); ok {
								if r, ok := b.(backend.Reloadable); ok {
									r.Reload()
								}
							}
						}
					}
				}
			}()

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

func registerBackends(ctx context.Context, cfg *config.Config, reg *backend.Registry, logger *slog.Logger, tracer trace.Tracer) error {
	// Validate that no plugin block label collides with a compiled-in type name.
	for _, pb := range cfg.Plugins {
		if _, reserved := compiledInTypes[pb.Name]; reserved {
			return fmt.Errorf("plugin %q: label is reserved by a compiled-in provider type", pb.Name)
		}
	}
	// Build a lookup map from plugin label to PluginBlock.
	pluginMap := make(map[string]config.PluginBlock, len(cfg.Plugins))
	for _, pb := range cfg.Plugins {
		pluginMap[pb.Name] = pb
	}

	// Read the lock file once so buildSidecarBackend can use the resolved
	// concrete version instead of the raw constraint string.
	var lf *lockfile.Lockfile
	if len(cfg.Plugins) > 0 {
		lockPath, lockErr := lockfile.PathFor(cfg.SourcePath)
		if lockErr == nil {
			if read, readErr := readOrNewLockfile(lockPath); readErr == nil {
				lf = read
			}
		}
	}
	if lf == nil {
		lf = &lockfile.Lockfile{}
	}

	logBodies := cfg.Server.LogBodies
	for _, b := range cfg.Backends {
		p, err := buildBackend(ctx, b, pluginMap, lf, logger, logBodies, tracer)
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
// the single source of truth for the type->constructor mapping used by the
// `serve` command.
//
// pluginMap maps plugin labels to their PluginBlock configs. When a backend's
// type matches a plugin label, a sidecar backend is returned if the plugin
// binary is installed; otherwise an error is returned directing the operator
// to run `llm-proxy plugin install`.
func buildBackend(ctx context.Context, b config.BackendBlock, pluginMap map[string]config.PluginBlock, lf *lockfile.Lockfile, logger *slog.Logger, logBodies bool, tracer trace.Tracer) (backend.Backend, error) {
	switch b.Type {
	case openai.Type:
		return openai.New(b, openai.Options{Name: b.Name, Models: b.Models, Logger: logger, LogBodies: logBodies, Tracer: tracer})
	default:
		// Check if the type matches a declared plugin label.
		if pb, ok := pluginMap[b.Type]; ok {
			return buildSidecarBackend(ctx, b, pb, lf, logger)
		}
		return nil, fmt.Errorf("backend %q: unknown type %q", b.Name, b.Type)
	}
}

// buildSidecarBackend checks that the plugin binary is installed, picks a free
// port, starts the plugin process via pluginrunner, waits for it to become
// healthy, then returns a sidecar.Backend pointed at its real address.
func buildSidecarBackend(ctx context.Context, b config.BackendBlock, pb config.PluginBlock, lf *lockfile.Lockfile, logger *slog.Logger) (backend.Backend, error) {
	cacheBase, err := paths.CacheDir()
	if err != nil {
		return nil, fmt.Errorf("plugin %q: resolve cache dir: %w", pb.Name, err)
	}

	// Resolve the concrete version: prefer the lock file so we use the same
	// version that `plugin install` actually downloaded, rather than the raw
	// constraint string (e.g. "~> 1.0") which is never a valid directory name.
	concreteVersion := pb.Version
	if entry, ok := lf.Lookup(pb.Name); ok {
		concreteVersion = entry.Resolved
	}

	namespace, name := sourceNamespace(pb.Source, pb.Name)
	destDir, err := plugin.PluginCacheDir(cacheBase, namespace, name, concreteVersion)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: cache dir: %w", pb.Name, err)
	}
	binPath := filepath.Join(destDir, plugin.BinaryName(pb.Name))
	if _, err := os.Stat(binPath); err != nil {
		return nil, fmt.Errorf(
			"plugin %q: binary not found at %q — run `llm-proxy plugin install` first",
			pb.Name, binPath,
		)
	}

	// Checksum verification: if the lock file recorded a checksum, verify the
	// on-disk binary has not been tampered with.
	if entry, ok := lf.Lookup(pb.Name); ok && entry.Checksum != "" {
		actualHex, hashErr := hashFile(binPath)
		if hashErr != nil {
			return nil, fmt.Errorf("plugin %q: hash binary: %w", pb.Name, hashErr)
		}
		if !strings.EqualFold(actualHex, entry.Checksum) {
			return nil, fmt.Errorf(
				"plugin %q: checksum mismatch — binary at %q has been modified (got %s, lock file expects %s); run `llm-proxy plugin install` to reinstall",
				pb.Name, binPath, actualHex, entry.Checksum,
			)
		}
	}

	// Read the manifest so we can honour the plugin's declared port env var
	// and endpoint paths.
	manifestPath := filepath.Join(destDir, plugin.ManifestName(pb.Name))
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: read manifest %q: %w", pb.Name, manifestPath, err)
	}
	mani, err := plugin.ParseManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("plugin %q: parse manifest: %w", pb.Name, err)
	}

	// Pick a free loopback port for the sidecar.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("plugin %q: allocate port: %w", pb.Name, err)
	}
	assignedPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	runner := pluginrunner.New(pluginrunner.Config{
		Name:                  pb.Name,
		BinaryPath:            binPath,
		PortEnvVar:            mani.PortEnvVar,
		AssignedPort:          assignedPort,
		HealthPath:            mani.Endpoints.Health,
		HealthInterval:        200 * time.Millisecond,
		HealthTimeout:         5 * time.Second,
		HealthThreshold:       1,
		ReloadPath:            mani.Endpoints.Reload,
		RestartPolicy:         pluginrunner.RestartOnFailure,
		RestartMaxRetries:     5,
		RestartInitialBackoff: time.Second,
		RestartBackoffCap:     30 * time.Second,
		ShutdownTimeout:       30 * time.Second,
	})

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := runner.Start(startCtx); err != nil {
		return nil, fmt.Errorf("plugin %q: %w", pb.Name, err)
	}

	logger.Info("plugin started", "plugin", pb.Name, "backend", b.Name, "addr", runner.BaseURL())

	return sidecar.New(sidecar.Options{
		Name:       b.Name,
		BaseURL:    runner.BaseURL(),
		BinaryPath: binPath,
		Tools:      mani.Tools,
	})
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
