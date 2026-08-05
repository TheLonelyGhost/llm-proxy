package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/thelonelyghost/llm-proxy/internal/config"
	"github.com/thelonelyghost/llm-proxy/internal/paths"
	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// newIBMBobCmd returns the `ibm-bob` command group: provider-specific
// helpers that do not require a running proxy.
func newIBMBobCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ibm-bob",
		Short: "Maintainer helpers for the IBM Bob backend",
		Long: "Subcommands that operate on the IBM Bob backend - " +
			"including credential management and secret extraction " +
			"(see docs/providers/ibm-bob.md).",
	}
	cmd.AddCommand(newIBMBobExtractCmd(out))
	cmd.AddCommand(newIBMBobLoginCmd(out))
	return cmd
}

// newIBMBobExtractCmd implements `llm-proxy ibm-bob extract [path]`.
//
// It mirrors the reference Python helper scripts/extract_secrets.py in the
// litellm-ibm-bob repository: locate the bob bundle (PATH lookup by default,
// or the supplied positional argument), recover the embedded version and
// AES-encrypted BUILD_TIME_HMAC_SECRET, and emit them as JSON.
func newIBMBobExtractCmd(out io.Writer) *cobra.Command {
	var (
		outputPath string
		envFormat  bool
	)
	cmd := &cobra.Command{
		Use:   "extract [path]",
		Short: "Extract the version and HMAC secret from an installed bob CLI",
		Long: "Locates the bob CLI (looked up on $PATH by default, or the " +
			"path passed as the only positional argument), decrypts the " +
			"BUILD_TIME_HMAC_SECRET embedded in its bundle, and prints both " +
			"the bob version and the secret as JSON.\n\n" +
			"Use --env to emit shell `export` statements suitable for sourcing.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			path, err := resolveBobPath(args)
			if err != nil {
				return err
			}
			secrets, err := ibmbob.Extract(path)
			if err != nil {
				return fmt.Errorf("extract from %q: %w", path, err)
			}
			return writeExtractResult(out, outputPath, envFormat, secrets)
		},
	}
	cmd.Flags().StringVarP(&outputPath, "output", "o", "",
		"write the JSON to this path instead of stdout (mode 0600)")
	cmd.Flags().BoolVar(&envFormat, "env", false,
		"emit `export NAME=value` lines (BOB_VERSION, BOB_HMAC_SECRET) instead of JSON")
	return cmd
}

// resolveBobPath returns the operator-supplied path or, when none is given,
// the result of looking up "bob" on $PATH. The returned path is not
// canonicalised here - Extract evaluates symlinks itself.
func resolveBobPath(args []string) (string, error) {
	if len(args) == 1 {
		return args[0], nil
	}
	path, err := exec.LookPath("bob")
	if err != nil {
		return "", fmt.Errorf("`bob` not found on $PATH; pass an explicit path as the positional argument")
	}
	return path, nil
}

func writeExtractResult(out io.Writer, outputPath string, env bool, s ibmbob.ExtractedSecrets) error {
	var payload []byte
	if env {
		payload = []byte(fmt.Sprintf(
			"export BOB_VERSION=%q\nexport BOB_HMAC_SECRET=%q\n",
			s.Version, s.BuildTimeHMACSecret,
		))
	} else {
		b, err := json.MarshalIndent(map[string]string{
			"version":                s.Version,
			"build_time_hmac_secret": s.BuildTimeHMACSecret,
		}, "", "  ")
		if err != nil {
			return fmt.Errorf("encode result: %w", err)
		}
		payload = make([]byte, 0, len(b)+1)
		payload = append(payload, b...)
		payload = append(payload, '\n')
	}
	if outputPath == "" {
		_, err := out.Write(payload)
		return err
	}
	// Secrets file: 0600, atomic via temp+rename would be overkill for a
	// one-shot CLI; use a direct write but enforce restrictive perms.
	if err := os.WriteFile(outputPath, payload, 0o600); err != nil {
		return fmt.Errorf("write %q: %w", outputPath, err)
	}
	fmt.Fprintf(out, "wrote %s (version=%s)\n", outputPath, s.Version) //nolint:errcheck
	return nil
}

// newIBMBobLoginCmd implements `llm-proxy ibm-bob login`.
//
// It runs the IBM Bob browser Authorization Code flow: starts a local
// callback server, prints (or opens) the login URL, waits for the browser
// redirect, exchanges the one-time code for a token pair, and writes it to
// the user's cache directory with mode 0600.
func newIBMBobLoginCmd(out io.Writer) *cobra.Command {
	var (
		cfgPath     string
		openBrowser bool
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with IBM Bob via browser SSO",
		Long: "Starts a local callback server, prints the IBM Bob SSO login URL\n" +
			"(or opens it in the default browser with --browser), waits for the\n" +
			"browser redirect, and exchanges the one-time code for a token pair\n" +
			"that is written to the user cache directory (mode 0600).\n\n" +
			"The running proxy picks up the new token on the next request or\n" +
			"immediately when it receives SIGHUP.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd, cfgPath)
			if err != nil {
				return err
			}
			block, err := findIBMBobBlock(cfg)
			if err != nil {
				return err
			}
			p, err := ibmbob.New(*block, ibmbob.Options{Name: block.Name, Models: block.Models})
			if err != nil {
				return err
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			return p.Login(ctx, func(loginURL string) {
				if openBrowser {
					if berr := openBrowserURL(loginURL); berr != nil {
						fmt.Fprintf(out, "Could not open browser (%v). Open this URL manually:\n\n  %s\n\n", berr, loginURL) //nolint:errcheck
						return
					}
				}
				fmt.Fprintf(out, "\nOpen this URL in your browser:\n\n  %s\n\nWaiting for authentication...\n", loginURL) //nolint:errcheck
			})
		},
	}
	var cfgDefault string
	if p, err2 := paths.ConfigFile(); err2 == nil {
		cfgDefault = p
	} else {
		cfgDefault = defaultConfigPath()
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", cfgDefault,
		"path to HCL config file or directory (or set LLM_PROXY_CONFIG)")
	cmd.Flags().BoolVarP(&openBrowser, "browser", "b", false,
		"open the IBM Bob SSO URL in the default browser automatically")
	return cmd
}

// findIBMBobBlock returns the first IBM Bob backend block from cfg.
func findIBMBobBlock(cfg *config.Config) (*config.BackendBlock, error) {
	for i, b := range cfg.Backends {
		if b.Type == ibmbob.Type {
			return &cfg.Backends[i], nil
		}
	}
	return nil, fmt.Errorf("no backend of type %q found in config", ibmbob.Type)
}

// openBrowserURL attempts to open url in the system default browser.
func openBrowserURL(rawURL string) error {
	var cmd *exec.Cmd
	switch {
	case commandExists("xdg-open"):
		cmd = exec.Command("xdg-open", rawURL) //nolint:gosec // operator-controlled URL for browser open
	case commandExists("open"):
		cmd = exec.Command("open", rawURL) //nolint:gosec // operator-controlled URL for browser open
	case commandExists("start"):
		cmd = exec.Command("cmd", "/c", "start", rawURL) //nolint:gosec // operator-controlled URL for browser open
	default:
		return fmt.Errorf("no browser-open command found")
	}
	return cmd.Start()
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
