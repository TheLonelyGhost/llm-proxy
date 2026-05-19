package cli

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveConfigPath_Precedence(t *testing.T) {
	t.Run("flag beats env", func(t *testing.T) {
		cmd := &cobra.Command{}
		cmd.Flags().StringP("config", "c", "", "")

		if err := cmd.Flags().Set("config", "/tmp/from-flag.hcl"); err != nil {
			t.Fatal(err)
		}
		t.Setenv(configEnvVar, "/tmp/from-env.hcl")
		if got := resolveConfigPath(cmd, "/tmp/from-flag.hcl"); got != "/tmp/from-flag.hcl" {
			t.Fatalf("flag precedence: got %q", got)
		}
	})

	t.Run("env beats default", func(t *testing.T) {
		cmd2 := &cobra.Command{}
		cmd2.Flags().StringP("config", "c", "", "")
		t.Setenv(configEnvVar, "/tmp/from-env.hcl")
		if got := resolveConfigPath(cmd2, ""); got != "/tmp/from-env.hcl" {
			t.Fatalf("env precedence: got %q", got)
		}
	})

	t.Run("default depends on os", func(t *testing.T) {
		cmd3 := &cobra.Command{}
		cmd3.Flags().StringP("config", "c", "", "")
		t.Setenv(configEnvVar, "")
		if got := resolveConfigPath(cmd3, ""); got != defaultConfigPathForGOOS("linux") {
			t.Fatalf("linux default: got %q", got)
		}
		if got := defaultConfigPathForGOOS("windows"); got != "C:/ProgramData/llm-proxy/config.hcl" {
			t.Fatalf("windows default: got %q", got)
		}
	})
}
