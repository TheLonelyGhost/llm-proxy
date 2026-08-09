// Command llm-proxy is the entry point for the OpenAI-compatible proxy.
//
// See `llm-proxy --help` for usage.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
)

func main() {
	root := cli.New(os.Stdout, os.Stderr)
	if err := root.Execute(); err != nil {
		// ExitCodeError means the plugin already wrote its diagnostics; just
		// forward the exit code without adding a second error message.
		var ece *cli.ExitCodeError
		if errors.As(err, &ece) {
			os.Exit(ece.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
