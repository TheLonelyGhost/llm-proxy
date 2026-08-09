// Package cli implements the proxy's command-line interface using Cobra.
//
// Subcommands:
//
//	serve   - start the HTTP proxy
//	plugin  - manage sidecar plugins (install, run)
//	version - print build version
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Version is overridden via -ldflags at release time.
var Version = "dev"

// New returns the root command. out receives normal (stdout) output;
// errOut receives diagnostic (stderr) output such as cobra error messages.
func New(out, errOut io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "llm-proxy",
		Short: "OpenAI-compatible proxy that fronts pluggable LLM backends",
		Long: "llm-proxy receives HTTP requests on the OpenAI API and forwards " +
			"them to configured backend providers, injecting backend-side credentials " +
			"the proxy holds (not the client's).",
		SilenceUsage: true,
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.AddCommand(newServeCmd(out))
	root.AddCommand(newProvidersCmd(out))
	root.AddCommand(newPluginCmd(out, errOut))
	root.AddCommand(newVersionCmd(out))
	return root
}

func newVersionCmd(out io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and exit",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(out, "llm-proxy", Version)
			return err
		},
	}
}
