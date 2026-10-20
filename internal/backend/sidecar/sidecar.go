// Package sidecar implements a backend.Backend that proxies requests to an
// out-of-process HTTP sidecar plugin over localhost.
//
// The sidecar must expose an OpenAI-compatible HTTP API. llm-proxy manages the
// sidecar process lifecycle; this package is solely responsible for the HTTP
// routing layer between the proxy and the running sidecar.
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/plugin"
)

// Options configures a sidecar Backend.
type Options struct {
	// Name is the Backend Name used for routing. Must be non-empty.
	Name string
	// BaseURL is the HTTP base URL of the sidecar process (e.g.
	// "http://127.0.0.1:9001"). Must be non-empty.
	BaseURL string
	// HTTPClient overrides the default HTTP client. If nil a client with no
	// total timeout is used, so streamed and slow responses are not cut off;
	// each request is bounded by the context passed to it instead.
	HTTPClient *http.Client
	// BinaryPath is the absolute path to the plugin binary. Required for
	// RunTool; may be empty when tool invocation is not needed.
	BinaryPath string
	// Tools lists the tooling subcommands declared in the plugin's manifest.
	// RunTool returns ErrToolNotFound for any name not in this list.
	Tools []plugin.Tool
}

// Backend is a backend.Backend implementation that forwards requests to a
// running sidecar HTTP process.
type Backend struct {
	name       string
	baseURL    string
	client     *http.Client
	binaryPath string
	tools      map[string]struct{} // set of registered tool names from manifest
}

// New constructs a sidecar Backend. Returns an error if required options are
// missing.
func New(opts Options) (*Backend, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("sidecar.New: Name is required")
	}
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("sidecar.New: BaseURL is required")
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	toolSet := make(map[string]struct{}, len(opts.Tools))
	for _, t := range opts.Tools {
		toolSet[t.Name] = struct{}{}
	}
	return &Backend{
		name:       opts.Name,
		baseURL:    strings.TrimRight(opts.BaseURL, "/"),
		client:     client,
		binaryPath: opts.BinaryPath,
		tools:      toolSet,
	}, nil
}

// Name implements backend.Backend.
func (b *Backend) Name() string { return b.name }

// Models calls GET /v1/models on the sidecar and returns the decoded model list.
func (b *Backend) Models(ctx context.Context) ([]backend.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("sidecar %q models request: %w", b.name, err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sidecar %q models: %w", b.name, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("sidecar %q models: nil response", b.name)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("sidecar %q models decode: %w", b.name, err)
	}
	models := make([]backend.ModelInfo, len(payload.Data))
	for i, d := range payload.Data {
		models[i] = modelInfoFromSidecar(d.ID, d.OwnedBy)
	}
	return models, nil
}

// Chat implements backend.Backend by proxying to /v1/chat/completions.
func (b *Backend) Chat(ctx context.Context, req backend.ChatRequest) (*backend.Response, error) {
	return b.forward(ctx, "/v1/chat/completions", req.Body)
}

// Complete implements backend.Backend by proxying to /v1/completions.
func (b *Backend) Complete(ctx context.Context, req backend.CompletionRequest) (*backend.Response, error) {
	return b.forward(ctx, "/v1/completions", req.Body)
}

// Embed implements backend.Backend by proxying to /v1/embeddings.
func (b *Backend) Embed(ctx context.Context, req backend.EmbeddingRequest) (*backend.Response, error) {
	return b.forward(ctx, "/v1/embeddings", req.Body)
}

// Responses implements backend.Backend by proxying to /v1/responses.
func (b *Backend) Responses(ctx context.Context, req backend.ResponsesRequest) (*backend.Response, error) {
	return b.forward(ctx, "/v1/responses", req.Body)
}

// forward performs a POST to the sidecar at path with body, translating a 501
// response into backend.ErrUnsupported.
func (b *Backend) forward(ctx context.Context, path string, body []byte) (*backend.Response, error) {
	url := b.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("sidecar %q %s request: %w", b.name, path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sidecar %q %s: %w", b.name, path, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("sidecar %q %s: nil response", b.name, path)
	}

	if resp.StatusCode == http.StatusNotImplemented {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w (sidecar %q, path %s)", backend.ErrUnsupported, b.name, path)
	}

	return &backend.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       resp.Body,
	}, nil
}

// forward GET for Models — kept internal; exported methods call forward or
// their own GET variant above.

// RunTool implements backend.ToolRunner by executing the plugin binary with
// --tool <toolName> and capturing stdout. Returns backend.ErrToolNotFound
// when the tool is not in the manifest, or an error if the process fails.
func (b *Backend) RunTool(ctx context.Context, toolName string) ([]byte, error) {
	if _, ok := b.tools[toolName]; !ok {
		return nil, fmt.Errorf("%w: %q not registered by plugin %q", backend.ErrToolNotFound, toolName, b.name)
	}
	if b.binaryPath == "" {
		return nil, fmt.Errorf("sidecar %q: BinaryPath is required to run tools", b.name)
	}
	cmd := exec.CommandContext(ctx, b.binaryPath, "--tool", toolName) //nolint:gosec // operator-controlled binary path
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit: return whatever stdout we captured alongside the
			// error so callers can inspect partial output if needed.
			return out, fmt.Errorf("plugin %q tool %q exited with status %d: %w", b.name, toolName, exitErr.ExitCode(), err)
		}
		return nil, fmt.Errorf("plugin %q tool %q: %w", b.name, toolName, err)
	}
	return out, nil
}

// Ensure Backend satisfies the interfaces at compile time.
var _ backend.Backend = (*Backend)(nil)
var _ backend.ToolRunner = (*Backend)(nil)

// modelInfoFromSidecar converts a raw sidecar model entry to backend.ModelInfo.
func modelInfoFromSidecar(id, ownedBy string) backend.ModelInfo {
	return backend.ModelInfo{
		ID:     id,
		Vendor: ownedBy,
	}
}
