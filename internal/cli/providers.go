package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// newProvidersCmd returns the `providers` command group: read-only helpers
// that introspect configured backends without starting the HTTP server.
func newProvidersCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "providers",
		Short: "Inspect configured backend providers",
		Long: "Read-only helpers that query configured backend providers " +
			"without starting the proxy. Useful for verifying credentials " +
			"and discovering which models the upstream catalogue currently " +
			"exposes.",
	}
	cmd.AddCommand(newProvidersListModelsCmd(out))
	return cmd
}

// newProvidersListModelsCmd implements `providers list-models <name>`.
//
// The positional argument is the backend instance name (HCL block label).
// The command loads the HCL config, instantiates the matching backend,
// asserts it implements backend.ModelLister, and renders the upstream
// catalogue. Backends decide which entries to drop (e.g. policy-disabled
// Copilot models); the CLI is otherwise a thin presenter.
func newProvidersListModelsCmd(out io.Writer) *cobra.Command {
	var (
		cfgPath string
		verbose bool
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "list-models <backend>",
		Short: "List models a configured backend's upstream catalogue exposes",
		Long: "Queries the upstream model catalogue for the named backend using " +
			"the credentials the proxy has stored. The positional argument is " +
			"the HCL backend block label (for example `ibm-bob` when your config " +
			"contains `backend \"ibm-bob\" { ... }`).\n\n" +
			"Without --verbose only model ids are printed, one per line. With " +
			"--verbose each model is followed by supplemental metadata reported " +
			"by the backend (vendor, capabilities, provider-specific extras).\n\n" +
			"Backends that do not implement upstream listing return a clear error.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			name := args[0]
			block, err := findBackendBlock(cfg, name)
			if err != nil {
				return err
			}
			pluginMap := make(map[string]config.PluginBlock, len(cfg.Plugins))
			for _, pb := range cfg.Plugins {
				pluginMap[pb.Name] = pb
			}
			b, err := buildBackend(cmd.Context(), *block, pluginMap, slog.Default(), cfg.Server.LogBodies, tracenoop.NewTracerProvider().Tracer("providers"))
			if err != nil {
				return fmt.Errorf("init backend %q: %w", block.Name, err)
			}
			//nolint:staticcheck // SA1019: ModelLister is deprecated but still supported for backward compatibility
			lister, ok := b.(backend.ModelLister)
			if !ok {
				return fmt.Errorf("backend %q (type %q) does not support upstream model listing",
					block.Name, block.Type)
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			models, err := lister.ListUpstreamModels(ctx)
			if err != nil {
				return err
			}
			return renderModels(out, models, verbose, jsonOut)
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
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false,
		"print supplemental metadata for each model")
	cmd.Flags().BoolVar(&jsonOut, "json", false,
		"emit JSON instead of plain text (implies --verbose)")
	return cmd
}

// findBackendBlock looks up a configured backend by its HCL block label.
// The label is the user-chosen routing prefix; matching is case-sensitive
// because the same convention is used for request routing.
func findBackendBlock(cfg *config.Config, name string) (*config.BackendBlock, error) {
	for i, b := range cfg.Backends {
		if b.Name == name {
			return &cfg.Backends[i], nil
		}
	}
	available := make([]string, 0, len(cfg.Backends))
	for _, b := range cfg.Backends {
		available = append(available, b.Name)
	}
	sort.Strings(available)
	return nil, fmt.Errorf("no backend %q in config (available: %s)",
		name, strings.Join(available, ", "))
}

// renderModels writes the upstream catalogue in either plain-text or JSON
// form. In plain mode, --verbose adds indented metadata lines per model.
func renderModels(out io.Writer, models []backend.ModelInfo, verbose, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(models)
	}
	for _, m := range models {
		if !verbose {
			if _, err := fmt.Fprintln(out, m.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(out, m.ID); err != nil {
			return err
		}
		if m.Name != "" {
			fmt.Fprintf(out, "    name: %s\n", m.Name) //nolint:errcheck
		}
		if m.Vendor != "" {
			fmt.Fprintf(out, "    vendor: %s\n", m.Vendor) //nolint:errcheck
		}
		if m.Version != "" {
			fmt.Fprintf(out, "    version: %s\n", m.Version) //nolint:errcheck
		}
		if len(m.Capabilities) > 0 {
			fmt.Fprintf(out, "    capabilities: %s\n", strings.Join(m.Capabilities, ", ")) //nolint:errcheck
		}
		if m.Deprecated {
			fmt.Fprintln(out, "    deprecated: true") //nolint:errcheck
		}
		if len(m.Extras) > 0 {
			keys := make([]string, 0, len(m.Extras))
			for k := range m.Extras {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(out, "    %s: %s\n", k, m.Extras[k]) //nolint:errcheck
			}
		}
	}
	return nil
}
