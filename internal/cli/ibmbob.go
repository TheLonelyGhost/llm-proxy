package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// newIBMBobCmd returns the `ibm-bob` command group: provider-specific
// helpers that do not require a running proxy.
func newIBMBobCmd(out io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ibm-bob",
		Short: "Maintainer helpers for the IBM Bob backend",
		Long: "Subcommands that operate on a locally installed `bob` CLI - " +
			"intended for one-off use when configuring the IBM Bob backend " +
			"(see docs/providers/ibm-bob.md).",
	}
	cmd.AddCommand(newIBMBobExtractCmd(out))
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
