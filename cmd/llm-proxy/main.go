// Command llm-proxy is the entry point for the OpenAI-compatible proxy.
//
// See `llm-proxy --help` for usage.
package main

import (
	"fmt"
	"os"

	"github.com/thelonelyghost/llm-proxy/internal/cli"
)

func main() {
	root := cli.New(os.Stdout)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
