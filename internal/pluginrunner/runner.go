// Package pluginrunner manages the lifecycle of out-of-process sidecar plugin
// binaries for llm-proxy.
//
// Each Runner owns one plugin process: it launches it with an assigned port,
// polls /healthz until the sidecar is ready, monitors it for crashes and
// applies the configured restart policy, and shuts it down cleanly on Stop.
// SIGHUP propagation is handled via Reload, which sends POST to the sidecar's
// reload endpoint.
package pluginrunner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// RestartPolicy controls what the Runner does when the plugin process exits
// unexpectedly.
type RestartPolicy int

const (
	// RestartOnFailure restarts the plugin on non-zero exit, with exponential
	// backoff up to RestartBackoffCap. Default policy.
	RestartOnFailure RestartPolicy = iota
	// RestartAlways restarts the plugin regardless of exit code.
	RestartAlways
	// RestartNever does not restart the plugin; the backend becomes
	// unavailable until the proxy is restarted.
	RestartNever
)

// String returns the canonical string representation of the policy.
func (p RestartPolicy) String() string {
	switch p {
	case RestartAlways:
		return "always"
	case RestartOnFailure:
		return "on-failure"
	case RestartNever:
		return "never"
	default:
		return "unknown"
	}
}

// ParseRestartPolicy parses a restart policy string. An empty string returns
// RestartOnFailure (the default). Returns an error for unrecognised values.
func ParseRestartPolicy(s string) (RestartPolicy, error) {
	switch s {
	case "", "on-failure":
		return RestartOnFailure, nil
	case "always":
		return RestartAlways, nil
	case "never":
		return RestartNever, nil
	default:
		return 0, fmt.Errorf("unknown restart policy %q: must be always, on-failure, or never", s)
	}
}

// Config holds the configuration for a single plugin Runner.
type Config struct {
	// Name is the plugin label, used in log and error messages.
	Name string
	// BinaryPath is the absolute path to the plugin binary.
	BinaryPath string
	// PortEnvVar is the environment variable used to pass the assigned port to
	// the plugin process. Defaults to "LLM_PROXY_PLUGIN_PORT".
	PortEnvVar string
	// AssignedPort is the TCP port the plugin should bind to.
	AssignedPort int
	// Env lists extra "KEY=value" entries appended to the inherited
	// environment of the plugin process. PortEnvVar is set after Env, so an
	// entry in Env cannot override the assigned port.
	Env []string

	// HealthPath is the HTTP path polled for readiness/liveness (e.g. "/healthz").
	HealthPath string
	// HealthInterval is how often to poll the health endpoint.
	HealthInterval time.Duration
	// HealthTimeout is the per-request timeout for health polls.
	HealthTimeout time.Duration
	// HealthThreshold is the number of consecutive failures before the sidecar
	// is considered dead.
	HealthThreshold int

	// ReloadPath is the HTTP path called on SIGHUP propagation
	// (e.g. "/management/reload").
	ReloadPath string

	// RestartPolicy controls automatic restart behaviour.
	RestartPolicy RestartPolicy
	// RestartMaxRetries is the maximum number of restart attempts.
	RestartMaxRetries int
	// RestartInitialBackoff is the initial backoff delay between restarts.
	RestartInitialBackoff time.Duration
	// RestartBackoffCap is the maximum backoff delay.
	RestartBackoffCap time.Duration

	// ShutdownTimeout is the total time budget for draining and stopping the
	// plugin on Stop.
	ShutdownTimeout time.Duration
}

func (c *Config) applyDefaults() {
	if c.PortEnvVar == "" {
		c.PortEnvVar = "LLM_PROXY_PLUGIN_PORT"
	}
	if c.HealthPath == "" {
		c.HealthPath = "/healthz"
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = 10 * time.Second
	}
	if c.HealthTimeout <= 0 {
		c.HealthTimeout = 5 * time.Second
	}
	if c.HealthThreshold <= 0 {
		c.HealthThreshold = 3
	}
	if c.ReloadPath == "" {
		c.ReloadPath = "/management/reload"
	}
	if c.RestartMaxRetries <= 0 {
		c.RestartMaxRetries = 5
	}
	if c.RestartInitialBackoff <= 0 {
		c.RestartInitialBackoff = time.Second
	}
	if c.RestartBackoffCap <= 0 {
		c.RestartBackoffCap = 60 * time.Second
	}
	if c.ShutdownTimeout <= 0 {
		c.ShutdownTimeout = 30 * time.Second
	}
}

// Runner manages the lifecycle of a single plugin process.
type Runner struct {
	cfg    Config
	mu     sync.Mutex
	cmd    *exec.Cmd
	client *http.Client
}

// New creates a Runner for the given Config. Defaults are applied; Start must
// be called to launch the process.
func New(cfg Config) *Runner {
	cfg.applyDefaults()
	return &Runner{
		cfg:    cfg,
		client: &http.Client{},
	}
}

// BaseURL returns the HTTP base URL of the plugin sidecar.
func (r *Runner) BaseURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(r.cfg.AssignedPort)
}

// Start launches the plugin process and waits until its health endpoint
// returns 200, or until ctx is cancelled. Returns an error if the process
// fails to launch or never becomes healthy.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cfg.BinaryPath == "" {
		return fmt.Errorf("plugin %q: BinaryPath is required", r.cfg.Name)
	}

	cmd := exec.Command(r.cfg.BinaryPath) //nolint:gosec // operator-controlled binary path
	cmd.Env = append(cmd.Environ(), r.cfg.Env...)
	cmd.Env = append(cmd.Env, r.cfg.PortEnvVar+"="+strconv.Itoa(r.cfg.AssignedPort))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("plugin %q: start process: %w", r.cfg.Name, err)
	}
	r.cmd = cmd

	if err := r.waitHealthy(ctx); err != nil {
		// Process started but never became healthy — kill it.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("plugin %q: %w", r.cfg.Name, err)
	}
	return nil
}

// waitHealthy polls the health endpoint until it returns 200 or ctx is done.
func (r *Runner) waitHealthy(ctx context.Context) error {
	healthURL := r.BaseURL() + r.cfg.HealthPath
	ticker := time.NewTicker(r.cfg.HealthInterval)
	defer ticker.Stop()

	var consecutive int
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("health check timed out waiting for readiness: %w", ctx.Err())
		case <-ticker.C:
			if r.checkHealth(ctx, healthURL) {
				consecutive++
			} else {
				consecutive = 0
			}
			// For initial readiness we require just one success.
			if consecutive >= 1 {
				return nil
			}
		}
	}
}

// checkHealth performs a single GET to healthURL and returns true on HTTP 200.
func (r *Runner) checkHealth(ctx context.Context, healthURL string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, r.cfg.HealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return false
	}
	resp, err := r.client.Do(req)
	if err != nil || resp == nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// Stop signals the plugin process to shut down and waits for it to exit,
// respecting ShutdownTimeout. If the process does not exit in time, it is
// killed.
func (r *Runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	cmd := r.cmd
	r.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// Send SIGTERM.
	if err := cmd.Process.Signal(sigterm()); err != nil {
		// Process may have already exited.
		if errors.Is(err, exec.ErrNotFound) {
			return nil
		}
	}

	// Wait with timeout, then SIGKILL. The exit status of a signalled process
	// is not an error for Stop, so only the exit itself is observed.
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	deadline := time.After(r.cfg.ShutdownTimeout)
	select {
	case <-done:
		return nil
	case <-deadline:
		_ = cmd.Process.Kill()
		<-done
		return nil
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

// Reload sends POST to the sidecar's reload endpoint, propagating a SIGHUP.
// Returns an error if the sidecar responds with a non-2xx status.
func (r *Runner) Reload(ctx context.Context) error {
	url := r.BaseURL() + r.cfg.ReloadPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("plugin %q reload request: %w", r.cfg.Name, err)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("plugin %q reload: %w", r.cfg.Name, err)
	}
	if resp == nil {
		return fmt.Errorf("plugin %q reload: nil response", r.cfg.Name)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("plugin %q reload: unexpected status %d", r.cfg.Name, resp.StatusCode)
	}
	return nil
}
