package cli

import (
	"os"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/thelonelyghost/llm-proxy/internal/config"
)

const configEnvVar = "LLM_PROXY_CONFIG"

func defaultConfigPathForGOOS(goos string) string {
	if goos == "windows" {
		return "C:/ProgramData/llm-proxy/config.hcl"
	}
	return "/etc/llm-proxy/config.hcl"
}

func defaultConfigPath() string {
	return defaultConfigPathForGOOS(runtime.GOOS)
}

func resolveConfigPath(cmd *cobra.Command, flagValue string) string {
	if cmd.Flags().Changed("config") {
		return flagValue
	}
	if envPath := os.Getenv(configEnvVar); envPath != "" {
		return envPath
	}
	if flagValue != "" {
		return flagValue
	}
	return defaultConfigPath()
}

func loadConfig(cmd *cobra.Command, flagValue string) (*config.Config, error) {
	return config.Load(resolveConfigPath(cmd, flagValue))
}
