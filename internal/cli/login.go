package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	"github.com/thelonelyghost/llm-proxy/internal/providers/copilot"
)

func newLoginCmd(out io.Writer) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authorise the proxy with a backend (currently: GitHub Copilot)",
		Long: "Runs an interactive GitHub device-code OAuth flow once and " +
			"persists the resulting token under the user's config dir with mode 0600. " +
			"Subsequent `serve` invocations reuse this credential.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			block, err := findCopilotBlock(cfg)
			if err != nil {
				return err
			}
			provider, err := copilot.New(*block, copilot.Options{Name: block.Name, Models: block.Models})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			code, err := provider.Auth().RequestDeviceCode(ctx)
			if err != nil {
				return err
			}
			expiresIn := time.Duration(code.ExpiresIn) * time.Second
			fmt.Fprintf(out, //nolint:errcheck // output writer is best-effort
				"\nOpen the following URL in your browser and enter the code:\n\n  %s\n\n  code: %s\n\nThis code expires in %s.\n\n",
				code.VerificationURI, code.UserCode, expiresIn)

			tok, err := provider.Auth().PollForToken(ctx, code)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Authorisation successful. Token stored.\n  scope: %s\n", tok.Scope) //nolint:errcheck
			return nil
		},
	}
	var cfgDefault string
	if p, err := paths.ConfigFile(); err == nil {
		cfgDefault = p
	} else {
		cfgDefault = defaultConfigPath()
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", cfgDefault, "path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	return cmd
}

func newLogoutCmd(out io.Writer) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove any persisted backend credentials",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			block, err := findCopilotBlock(cfg)
			if err != nil {
				return err
			}
			provider, err := copilot.New(*block, copilot.Options{Name: block.Name, Models: block.Models})
			if err != nil {
				return err
			}
			if err := provider.Auth().Logout(); err != nil {
				return err
			}
			fmt.Fprintln(out, "Stored credentials removed.") //nolint:errcheck
			return nil
		},
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", defaultConfigPath(), "path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	return cmd
}

func findCopilotBlock(cfg *config.Config) (*config.BackendBlock, error) {
	for i, b := range cfg.Backends {
		if b.Type == copilot.Type {
			return &cfg.Backends[i], nil
		}
	}
	return nil, fmt.Errorf("no backend of type %q found in config", copilot.Type)
}
