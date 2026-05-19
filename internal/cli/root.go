// Package cli implements the proxy's command-line interface using Cobra.
//
// Subcommands:
//
//	serve   - start the HTTP proxy
//	login   - run the GitHub device-code flow once to persist credentials
//	version - print build version
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Version is overridden via -ldflags at release time.
var Version = "dev"

// New returns the root command.
func New(out io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:   "llm-proxy",
		Short: "OpenAI-compatible proxy that fronts pluggable LLM backends",
		Long: "llm-proxy receives HTTP requests on the OpenAI API and forwards " +
			"them to configured backend providers, injecting backend-side credentials " +
			"the proxy holds (not the client's).",
		SilenceUsage: true,
	}
	root.SetOut(out)
	root.SetErr(out)
	root.AddCommand(newServeCmd(out))
	root.AddCommand(newLoginCmd(out))
	root.AddCommand(newLogoutCmd(out))
	root.AddCommand(newIBMBobCmd(out))
	root.AddCommand(newProvidersCmd(out))
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
