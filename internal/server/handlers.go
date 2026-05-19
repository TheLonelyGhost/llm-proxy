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

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	out := modelList{Object: "list"}
	for _, b := range s.registry.All() {
		models, err := b.Models(r.Context())
		if err != nil {
			s.logger.Warn("backend models error", "backend", b.Name(), "err", err)
			continue
		}
		for _, m := range models {
			out.Data = append(out.Data, modelListItem{
				ID:      router.Format(b.Name(), m.ID),
				Object:  "model",
				Created: 0,
				OwnedBy: m.OwnedBy,
			})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// extractModel decodes just enough of the body to find the "model" field,
// then rewrites it in place to the canonical (prefix-stripped) id and
// returns the parsed Route alongside the rewritten bytes.
func extractAndRewriteModel(body []byte) (router.Route, bool, []byte, error) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream,omitempty"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return router.Route{}, false, nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	if probe.Model == "" {
		return router.Route{}, false, nil, errors.New("request missing required field \"model\"")
	}
	route, err := router.Parse(probe.Model)
	if err != nil {
		return router.Route{}, false, nil, err
	}

	// Rewrite "model" in a generic map to preserve unknown fields.
	generic := map[string]any{}
	if err := json.Unmarshal(body, &generic); err != nil {
		return router.Route{}, false, nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	generic["model"] = route.Model
	rewritten, err := json.Marshal(generic)
	if err != nil {
		return router.Route{}, false, nil, fmt.Errorf("re-encode body: %w", err)
	}
	return route, probe.Stream, rewritten, nil
}

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", err.Error())
		return nil, false
	}
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
	s.handleModelRequest(w, r, func(ctx context.Context, b backend.Backend, route router.Route, body []byte, stream bool) (*backend.Response, error) {
		return b.Chat(ctx, backend.ChatRequest{Model: route.Model, Stream: stream, Body: body})
	})
}

func (s *Server) handleCompletion(w http.ResponseWriter, r *http.Request) {
	s.handleModelRequest(w, r, func(ctx context.Context, b backend.Backend, route router.Route, body []byte, stream bool) (*backend.Response, error) {
		return b.Complete(ctx, backend.CompletionRequest{Model: route.Model, Stream: stream, Body: body})
	})
}

func (s *Server) handleEmbedding(w http.ResponseWriter, r *http.Request) {
	s.handleModelRequest(w, r, func(ctx context.Context, b backend.Backend, route router.Route, body []byte, _ bool) (*backend.Response, error) {
		return b.Embed(ctx, backend.EmbeddingRequest{Model: route.Model, Body: body})
	})
}

func (s *Server) handleModelRequest(
	w http.ResponseWriter,
	r *http.Request,
	op func(context.Context, backend.Backend, router.Route, []byte, bool) (*backend.Response, error),
) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	route, stream, rewritten, err := extractAndRewriteModel(body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	b, ok := s.lookupBackend(w, route.Backend)
	if !ok {
		return
	}
	resp, err := op(r.Context(), b, route, rewritten, stream)
	s.writeUpstreamResponse(w, resp, err, stream)
}

// writeUpstreamResponse maps a backend.Response (or error) onto the client's
// http.ResponseWriter, preserving headers and streaming semantics.
func (s *Server) writeUpstreamResponse(w http.ResponseWriter, resp *backend.Response, err error, stream bool) {
	if err != nil {
		switch {
		case errors.Is(err, backend.ErrUnsupported):
			writeAPIError(w, http.StatusNotImplemented, "not_implemented", err.Error())
		case errors.Is(err, backend.ErrUnknownModel):
			writeAPIError(w, http.StatusNotFound, "invalid_request_error", err.Error())
		default:
			s.logger.Error("backend error", "err", err)
			writeAPIError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if resp == nil {
		writeAPIError(w, http.StatusBadGateway, "upstream_error", "empty response from backend")
		return
	}
	// Copy hop-by-hop-safe headers.
	for k, vv := range resp.Header {
		if isHopByHop(k) {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	copyBody(w, resp.Body, stream)
}

func isHopByHop(h string) bool {
	switch http.CanonicalHeaderKey(h) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	}
	return false
}
