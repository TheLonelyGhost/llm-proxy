// Package plugintest provides a conformance test harness for llm-proxy sidecar
// plugins.
//
// Plugin authors import this package in their test suite and call Run to verify
// their binary satisfies the plugin contract:
//
//	func TestConformance(t *testing.T) {
//	    plugintest.Run(t, plugintest.Options{
//	        Binary: "./bin/llm-proxy-provider-myprovider",
//	    })
//	}
//
// Run launches the binary, exercises all required endpoints, verifies that
// optional endpoints return 501 (not 404) when unsupported, and checks
// graceful shutdown behaviour.
package plugintest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// Options configures a conformance test run.
type Options struct {
	// Binary is the path to the plugin binary to test. Required.
	Binary string
	// PortEnvVar is the environment variable used to pass the port to the
	// plugin. Defaults to "LLM_PROXY_PLUGIN_PORT".
	PortEnvVar string
	// StartTimeout is how long to wait for the sidecar to become healthy.
	// Defaults to 10 seconds.
	StartTimeout time.Duration
	// ExtraEnv is additional environment variables to pass to the plugin process.
	ExtraEnv []string
}

func (o *Options) applyDefaults() {
	if o.PortEnvVar == "" {
		o.PortEnvVar = "LLM_PROXY_PLUGIN_PORT"
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 10 * time.Second
	}
}

// Run launches the plugin binary and runs the full conformance suite as
// sub-tests of t. It is the single entry point for plugin authors.
func Run(t *testing.T, opts Options) {
	t.Helper()
	opts.applyDefaults()

	if opts.Binary == "" {
		t.Fatal("plugintest.Run: Options.Binary is required")
	}

	port := freePort(t)
	cmd := exec.Command(opts.Binary) //nolint:gosec // test helper; operator-controlled binary
	cmd.Env = append(cmd.Environ(),
		opts.PortEnvVar+"="+strconv.Itoa(port),
	)
	cmd.Env = append(cmd.Env, opts.ExtraEnv...)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin binary %q: %v", opts.Binary, err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}

	// Wait for readiness.
	if !waitHealthy(t, client, baseURL+"/healthz", opts.StartTimeout) {
		t.Fatalf("plugin never became healthy at %s/healthz within %s", baseURL, opts.StartTimeout)
	}

	t.Run("healthz_returns_200", func(t *testing.T) {
		resp := mustGet(t, client, baseURL+"/healthz")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("management_reload_returns_2xx", func(t *testing.T) {
		resp := mustPost(t, client, baseURL+"/management/reload", nil)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.Errorf("POST /management/reload = %d, want 2xx", resp.StatusCode)
		}
	})

	t.Run("v1_models_returns_200", func(t *testing.T) {
		resp := mustGet(t, client, baseURL+"/v1/models")
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /v1/models = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("v1_chat_completions_returns_2xx", func(t *testing.T) {
		body := []byte(`{"model":"test","messages":[]}`)
		resp := mustPost(t, client, baseURL+"/v1/chat/completions", body)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			t.Errorf("POST /v1/chat/completions = %d, want 2xx", resp.StatusCode)
		}
	})

	t.Run("optional_endpoints_return_501_not_404", func(t *testing.T) {
		for _, path := range []string{"/v1/completions", "/v1/embeddings", "/v1/responses"} {
			t.Run(path, func(t *testing.T) {
				body := []byte(`{"model":"test"}`)
				resp := mustPost(t, client, baseURL+path, body)
				defer func() { _ = resp.Body.Close() }()
				if resp.StatusCode == http.StatusNotFound {
					t.Errorf("POST %s = 404; unsupported optional endpoints must return 501, not 404", path)
				}
				// 2xx (supported) or 501 (explicitly unsupported) are both acceptable.
				if resp.StatusCode != http.StatusNotImplemented &&
					(resp.StatusCode < 200 || resp.StatusCode >= 300) {
					t.Errorf("POST %s = %d; want 2xx or 501", path, resp.StatusCode)
				}
			})
		}
	})

	t.Run("graceful_shutdown", func(t *testing.T) {
		if cmd.Process == nil {
			t.Skip("process not running")
		}
		sigterm(t, cmd)
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
			// clean exit — pass
		case <-time.After(5 * time.Second):
			t.Error("plugin process did not exit within 5s after SIGTERM")
		}
	})
}

// waitHealthy polls the health endpoint until it returns 200 or the deadline
// passes.
func waitHealthy(t *testing.T, client *http.Client, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		resp, err := client.Do(req)
		if err == nil && resp != nil && resp.StatusCode == http.StatusOK {
			_ = resp.Body.Close()
			return true
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func mustGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func mustPost(t *testing.T, client *http.Client, url string, body []byte) *http.Response {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		buf = bytes.NewReader(body)
	} else {
		buf = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, buf)
	if err != nil {
		t.Fatalf("build POST %s: %v", url, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}
