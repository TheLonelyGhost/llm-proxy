package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"github.com/thelonelyghost/llm-proxy/internal/router"
	"github.com/thelonelyghost/llm-proxy/internal/transform"
)

// maxBodyBytes caps the size of incoming JSON bodies. 8 MiB is generous for
// chat payloads while preventing slowloris-style memory exhaustion.
const maxBodyBytes = 8 << 20

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"backends": s.registry.Names(),
		"time":     time.Now().UTC().Format(time.RFC3339),
	})
}

// modelListItem mirrors OpenAI's /v1/models entry.
type modelListItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type modelList struct {
	Object string          `json:"object"`
	Data   []modelListItem `json:"data"`
}

type modelInfoItem struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name,omitempty"`
	Vendor                string            `json:"vendor"`
	Capabilities          []string          `json:"capabilities,omitempty"`
	TokenLimits           *tokenLimits      `json:"token_limits,omitempty"`
	ReasoningEffortLevels []string          `json:"reasoning_effort_levels,omitempty"`
	Deprecated            bool              `json:"deprecated"`
	Version               string            `json:"version,omitempty"`
	Extras                map[string]string `json:"extras,omitempty"`
}

type tokenLimits struct {
	MaxInputTokens   int `json:"max_input_tokens,omitempty"`
	MaxOutputTokens  int `json:"max_output_tokens,omitempty"`
	MaxContextTokens int `json:"max_context_tokens,omitempty"`
}

type modelInfoList struct {
	Object string          `json:"object"`
	Data   []modelInfoItem `json:"data"`
}

// budgetItem is the per-backend payload returned under /v1/usage/budget.
// Field names mirror backend.Budget so the JSON shape is stable across
// the package boundary, with `backend` added so list responses are
// self-describing.
type budgetItem struct {
	Object    string            `json:"object"`
	Backend   string            `json:"backend"`
	Currency  string            `json:"currency,omitempty"`
	MaxBudget float64           `json:"max_budget"`
	Spend     float64           `json:"spend"`
	Remaining float64           `json:"remaining"`
	Unlimited bool              `json:"unlimited,omitempty"`
	Extras    map[string]string `json:"extras,omitempty"`
}

type budgetList struct {
	Object string       `json:"object"`
	Data   []budgetItem `json:"data"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	out := modelList{Object: "list", Data: []modelListItem{}}
	for _, b := range s.registry.All() {
		models, err := b.Models(r.Context())
		if err != nil {
			s.logger.Error("backend models error", "backend", b.Name(), "err", err)
			http.Error(w, fmt.Sprintf("backend %q models: %v", b.Name(), err), http.StatusBadGateway)
			return
		}
		for _, m := range models {
			out.Data = append(out.Data, modelListItem{
				ID:      router.Format(b.Name(), m.ID),
				Object:  "model",
				Created: 0,
				OwnedBy: b.Name(),
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleModelInfo(w http.ResponseWriter, r *http.Request) {
	out := modelInfoList{Object: "list", Data: []modelInfoItem{}}
	for _, b := range s.registry.All() {
		models, err := b.Models(r.Context())
		if err != nil {
			s.logger.Error("backend models error", "backend", b.Name(), "err", err)
			http.Error(w, fmt.Sprintf("backend %q models: %v", b.Name(), err), http.StatusBadGateway)
			return
		}
		for _, m := range models {
			item := modelInfoItem{
				ID:                    router.Format(b.Name(), m.ID),
				Name:                  m.Name,
				Vendor:                b.Name(),
				Capabilities:          m.Capabilities,
				ReasoningEffortLevels: m.ReasoningEffortLevels,
				Deprecated:            m.Deprecated,
				Version:               m.Version,
				Extras:                m.Extras,
			}

			if m.TokenLimits != nil {
				item.TokenLimits = &tokenLimits{
					MaxInputTokens:   m.TokenLimits.MaxInputTokens,
					MaxOutputTokens:  m.TokenLimits.MaxOutputTokens,
					MaxContextTokens: m.TokenLimits.MaxContextTokens,
				}
			}

			out.Data = append(out.Data, item)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleBudget reports per-backend spend / quota information. It tries each
// registered backend in turn using two strategies:
//
//  1. backend.BudgetReporter (compiled-in providers that implement it directly)
//  2. backend.ToolRunner + tool name "budget" (sidecar plugins that ship a
//     budget tool; stdout must validate against assets/budget.schema.json)
//
// The response is styled like other OpenAI list endpoints (object/data,
// snake_case) and includes a "backend" discriminator on every item.
//
// Query parameters:
//
//	backend=<prefix>  Restrict the response to a single backend. When
//	                  omitted, the proxy aggregates every registered
//	                  backend that supports budget reporting.
//
// Status codes:
//
//	200  one or more backends reported a budget
//	404  ?backend=<x> was supplied but no such backend is registered
//	501  the named backend supports neither BudgetReporter nor a budget tool,
//	     or no registered backend supports either
//	502  upstream failure from a backend that does support it
func (s *Server) handleBudget(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("backend")

	if name != "" {
		b, ok := s.lookupBackend(w, name)
		if !ok {
			return
		}
		item, status, msg := s.fetchBudgetItem(r.Context(), b)
		if status != 0 {
			s.writeBudgetHTTPError(w, name, status, msg)
			return
		}
		writeJSON(w, http.StatusOK, item)
		return
	}

	// Aggregate over every registered backend that supports budget reporting.
	// Backends that support neither BudgetReporter nor a budget tool are
	// skipped silently; upstream errors from those that do are logged and
	// skipped so the aggregate response remains useful.
	out := budgetList{Object: "list", Data: []budgetItem{}}
	saw := false
	for _, b := range s.registry.All() {
		if !supportsBudget(b) {
			continue
		}
		saw = true
		item, status, msg := s.fetchBudgetItem(r.Context(), b)
		if status != 0 {
			s.logger.Warn("budget fetch failed", "backend", b.Name(), "status", status, "msg", msg)
			continue
		}
		out.Data = append(out.Data, item)
	}
	if !saw {
		writeAPIError(w, http.StatusNotImplemented, "not_implemented",
			"no registered backend reports budget info")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// supportsBudget returns true when b implements BudgetReporter or ToolRunner
// with a registered "budget" tool.
func supportsBudget(b backend.Backend) bool {
	if _, ok := b.(backend.BudgetReporter); ok {
		return true
	}
	if tr, ok := b.(backend.ToolRunner); ok {
		_, err := tr.RunTool(context.Background(), "budget")
		return !errors.Is(err, backend.ErrToolNotFound)
	}
	return false
}

// fetchBudgetItem tries BudgetReporter then ToolRunner for a single backend.
// On success it returns (item, 0, ""). On failure it returns (zero, httpStatus, message).
func (s *Server) fetchBudgetItem(ctx context.Context, b backend.Backend) (budgetItem, int, string) {
	// Strategy 1: compiled-in BudgetReporter.
	if reporter, ok := b.(backend.BudgetReporter); ok {
		bud, err := reporter.Budget(ctx)
		if err != nil {
			if errors.Is(err, backend.ErrUnsupported) {
				return budgetItem{}, http.StatusNotImplemented, err.Error()
			}
			s.logger.Error("budget upstream error", "backend", b.Name(), "err", err)
			return budgetItem{}, http.StatusBadGateway, err.Error()
		}
		return budgetToItem(b.Name(), bud), 0, ""
	}

	// Strategy 2: ToolRunner with the "budget" tool.
	tr, ok := b.(backend.ToolRunner)
	if !ok {
		return budgetItem{}, http.StatusNotImplemented,
			fmt.Sprintf("backend %q does not report budget info", b.Name())
	}
	out, err := tr.RunTool(ctx, "budget")
	if err != nil {
		if errors.Is(err, backend.ErrToolNotFound) {
			return budgetItem{}, http.StatusNotImplemented,
				fmt.Sprintf("backend %q does not report budget info", b.Name())
		}
		// Non-zero exit: out may still contain a JSON error payload — try to
		// parse it before falling through to a generic 502.
		if item, parseErr := parseBudgetToolOutput(b.Name(), out); parseErr == nil {
			return item, 0, ""
		}
		s.logger.Error("budget tool error", "backend", b.Name(), "err", err)
		return budgetItem{}, http.StatusBadGateway, err.Error()
	}

	item, parseErr := parseBudgetToolOutput(b.Name(), out)
	if parseErr != nil {
		s.logger.Error("budget tool output invalid", "backend", b.Name(), "err", parseErr)
		return budgetItem{}, http.StatusBadGateway,
			fmt.Sprintf("backend %q budget tool returned invalid output: %v", b.Name(), parseErr)
	}
	return item, 0, ""
}

// parseBudgetToolOutput decodes raw stdout bytes from a budget tool invocation
// against the contract defined by assets/budget.schema.json.
//
// The schema's oneOf has two shapes, discriminated by the presence of "error":
//
//	BudgetResponse:      {"object":"usage.budget", "max_budget":…, "spend":…, "remaining":…, …}
//	BudgetErrorResponse: {"object":"usage.budget", "error":"<message>"}
//
// A BudgetErrorResponse is returned as a Go error so the caller can map it to
// an appropriate HTTP status.
func parseBudgetToolOutput(backendName string, data []byte) (budgetItem, error) {
	if len(data) == 0 {
		return budgetItem{}, fmt.Errorf("empty output")
	}

	// Decode into a flexible envelope first to read the discriminator fields.
	var env struct {
		Object    string            `json:"object"`
		Error     string            `json:"error"`
		Currency  string            `json:"currency"`
		MaxBudget float64           `json:"max_budget"`
		Spend     float64           `json:"spend"`
		Remaining float64           `json:"remaining"`
		Unlimited bool              `json:"unlimited"`
		Extras    map[string]string `json:"extras"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return budgetItem{}, fmt.Errorf("JSON decode: %w", err)
	}
	if env.Object != "usage.budget" {
		return budgetItem{}, fmt.Errorf("unexpected object %q (want \"usage.budget\")", env.Object)
	}
	// BudgetErrorResponse discriminator.
	if env.Error != "" {
		return budgetItem{}, fmt.Errorf("budget tool reported error: %s", env.Error)
	}
	return budgetItem{
		Object:    "usage.budget",
		Backend:   backendName,
		Currency:  env.Currency,
		MaxBudget: env.MaxBudget,
		Spend:     env.Spend,
		Remaining: env.Remaining,
		Unlimited: env.Unlimited,
		Extras:    env.Extras,
	}, nil
}

func (s *Server) writeBudgetHTTPError(w http.ResponseWriter, name string, status int, msg string) {
	switch status {
	case http.StatusNotImplemented:
		writeAPIError(w, http.StatusNotImplemented, "not_implemented", msg)
	default:
		s.logger.Error("budget upstream error", "backend", name, "msg", msg)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", msg)
	}
}

func budgetToItem(name string, bud backend.Budget) budgetItem {
	return budgetItem{
		Object:    "usage.budget",
		Backend:   name,
		Currency:  bud.Currency,
		MaxBudget: bud.MaxBudget,
		Spend:     bud.Spend,
		Remaining: bud.Remaining,
		Unlimited: bud.Unlimited,
		Extras:    bud.Extras,
	}
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", err.Error())
		return nil, false
	}
	recordRequestBody(r.Context(), body)
	return body, true
}

func (s *Server) lookupBackend(w http.ResponseWriter, name string) (backend.Backend, bool) {
	b, ok := s.registry.Get(name)
	if !ok || b == nil {
		writeAPIError(w, http.StatusNotFound, "invalid_request_error",
			fmt.Sprintf("no backend registered for prefix %q", name))
		return nil, false
	}
	return b, true
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	s.handleModelRequest(w, r, transform.EndpointChat,
		parseStandard,
		func(ctx context.Context, b backend.Backend, route router.Route, body []byte, stream bool) (*backend.Response, error) {
			return b.Chat(ctx, backend.ChatRequest{Model: route.Model, Stream: stream, Body: body})
		})
}

func (s *Server) handleCompletion(w http.ResponseWriter, r *http.Request) {
	s.handleModelRequest(w, r, transform.EndpointCompletion,
		parseStandard,
		func(ctx context.Context, b backend.Backend, route router.Route, body []byte, stream bool) (*backend.Response, error) {
			return b.Complete(ctx, backend.CompletionRequest{Model: route.Model, Stream: stream, Body: body})
		})
}

func (s *Server) handleEmbedding(w http.ResponseWriter, r *http.Request) {
	s.handleModelRequest(w, r, transform.EndpointEmbedding,
		parseStandard,
		func(ctx context.Context, b backend.Backend, route router.Route, body []byte, _ bool) (*backend.Response, error) {
			return b.Embed(ctx, backend.EmbeddingRequest{Model: route.Model, Body: body})
		})
}

// parseStandard adapts router.ParseAndRewrite to the parseFunc signature
// expected by handleModelRequest: (Route, body, stream, error).
func parseStandard(body []byte) (router.Route, []byte, bool, error) {
	route, stream, rewritten, err := router.ParseAndRewrite(body)
	return route, rewritten, stream, err
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	var parsed *backend.ResponsesRequest
	s.handleModelRequest(w, r, transform.EndpointResponses,
		func(body []byte) (router.Route, []byte, bool, error) {
			route, req, err := router.ParseResponsesRequest(body)
			if err != nil {
				return router.Route{}, nil, false, err
			}
			parsed = req
			return route, req.Body, req.Stream, nil
		},
		func(ctx context.Context, b backend.Backend, _ router.Route, _ []byte, _ bool) (*backend.Response, error) {
			return b.Responses(ctx, *parsed)
		})
}

func (s *Server) handleModelRequest(
	w http.ResponseWriter,
	r *http.Request,
	ep transform.Endpoint,
	parseBody func([]byte) (router.Route, []byte, bool, error),
	op func(context.Context, backend.Backend, router.Route, []byte, bool) (*backend.Response, error),
) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	route, rewritten, stream, err := parseBody(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	b, ok := s.lookupBackend(w, route.Backend)
	if !ok {
		return
	}
	resp, err := op(r.Context(), b, route, rewritten, stream)
	s.writeUpstreamResponse(w, resp, err, stream, ep, router.Format(b.Name(), route.Model))
}

// writeUpstreamResponse maps a backend.Response (or error) onto the client's
// http.ResponseWriter, applying transformation and preserving streaming semantics.
func (s *Server) writeUpstreamResponse(w http.ResponseWriter, resp *backend.Response, err error, stream bool, ep transform.Endpoint, modelPrefix string) {
	if err != nil {
		switch {
		case errors.Is(err, backend.ErrUnsupported):
			s.logger.Info("backend does not support this operation", "model", modelPrefix, "err", err)
			writeAPIError(w, http.StatusNotImplemented, "not_implemented", err.Error())
		case errors.Is(err, backend.ErrUnknownModel):
			s.logger.Info("model not found in backend allow-list", "model", modelPrefix, "err", err)
			writeAPIError(w, http.StatusNotFound, "invalid_request_error", err.Error())
		default:
			s.logger.Error("backend request failed", "model", modelPrefix, "err", err)
			writeAPIError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if resp == nil {
		s.logger.Error("backend returned nil response", "model", modelPrefix)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "empty response from backend")
		return
	}

	if resp.StatusCode >= 400 {
		s.logger.Info("upstream returned error status", "model", modelPrefix, "status", resp.StatusCode)
	}

	transformed, transformErr := transform.Transform(resp, ep, stream, modelPrefix)
	if transformErr != nil {
		s.logger.Error("response transformation failed", "model", modelPrefix, "status", resp.StatusCode, "err", transformErr)
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "failed to transform backend response")
		return
	}

	for k, vv := range transformed.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(transformed.StatusCode)
	copyBody(w, transformed.Body, stream)
}

func isHopByHop(h string) bool {
	switch http.CanonicalHeaderKey(h) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}
