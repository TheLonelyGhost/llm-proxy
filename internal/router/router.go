// Package router implements prefix-based model routing.
//
// Clients address backends through the model id they send. A model id of the
// form "<backend>/<canonical-model>" routes to the named backend with the
// canonical model id stripped of the prefix. For example:
//
//	"copilot/gpt-4o" -> backend "copilot", model "gpt-4o"
//
// Empty or prefix-less ids are rejected; the proxy refuses to silently pick a
// default backend to avoid accidental traffic routing.
package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
)

// ErrInvalidModelID is returned when a model id cannot be parsed.
var ErrInvalidModelID = errors.New("invalid model id: expected '<backend>/<model>'")

// Route is the parsed result of a model id.
type Route struct {
	Backend string // e.g. "copilot"
	Model   string // e.g. "gpt-4o"
}

// Parse splits a client-supplied model id into its backend prefix and the
// canonical model name expected by that backend.
//
// The split is made at the first '/' only; any subsequent '/' characters are
// preserved as part of the model name and forwarded to the upstream backend
// verbatim. Both halves must be non-empty after trimming surrounding
// whitespace. The backend half is lowercased to match registry keys.
func Parse(id string) (Route, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Route{}, ErrInvalidModelID
	}
	idx := strings.IndexByte(id, '/')
	if idx <= 0 || idx == len(id)-1 {
		return Route{}, ErrInvalidModelID
	}
	left := id[:idx]
	right := id[idx+1:]
	if strings.TrimSpace(left) != left || strings.TrimSpace(right) != right {
		return Route{}, ErrInvalidModelID
	}
	return Route{Backend: strings.ToLower(left), Model: right}, nil
}

// Format reverses Parse, producing the id form used by clients.
func Format(backend, model string) string {
	return backend + "/" + model
}

// ParseAndRewrite parses the Qualified Model ID from a JSON request body,
// strips the Backend Name prefix to produce the Canonical Model ID, and
// returns a copy of the body with the "model" field rewritten in place.
//
// A single json.Unmarshal into map[string]any is performed — no probe struct,
// no double-decode. The returned bool is the value of the "stream" field (or
// false when absent). The returned []byte is the re-encoded body with the
// canonical model id substituted.
func ParseAndRewrite(body []byte) (Route, bool, []byte, error) {
	if len(body) == 0 {
		return Route{}, false, nil, fmt.Errorf("invalid JSON body: empty")
	}
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		return Route{}, false, nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	modelVal, ok := generic["model"]
	if !ok {
		return Route{}, false, nil, errors.New("request missing required field \"model\"")
	}
	modelStr, ok := modelVal.(string)
	if !ok || modelStr == "" {
		return Route{}, false, nil, errors.New("request missing required field \"model\"")
	}
	route, err := Parse(modelStr)
	if err != nil {
		return Route{}, false, nil, err
	}
	generic["model"] = route.Model
	stream, _ := generic["stream"].(bool)
	rewritten, err := json.Marshal(generic)
	if err != nil {
		return Route{}, false, nil, fmt.Errorf("re-encode body: %w", err)
	}
	return route, stream, rewritten, nil
}

// validReasoningEfforts is the set of effort strings the proxy accepts for
// the responses endpoint reasoning parameter.
var validReasoningEfforts = map[string]bool{
	"none": true, "minimal": true, "low": true,
	"medium": true, "high": true, "xhigh": true, "max": true,
}

// ParseResponsesRequest fully parses and validates a /v1/responses request
// body. It performs the model-id rewrite (identical to ParseAndRewrite),
// validates the input field shape, and validates the optional reasoning.effort
// field. The returned Route and *backend.ResponsesRequest are ready to hand
// directly to backend.Backend.Responses; the Route carries the backend name
// needed for dispatch.
//
// All returned errors are client-facing (suitable for a 400 response body).
func ParseResponsesRequest(body []byte) (Route, *backend.ResponsesRequest, error) {
	route, stream, rewritten, err := ParseAndRewrite(body)
	if err != nil {
		return Route{}, nil, err
	}

	var raw struct {
		Input     json.RawMessage `json:"input"`
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Route{}, nil, fmt.Errorf("invalid JSON body: %w", err)
	}

	if err := validateResponsesInput(raw.Input); err != nil {
		return Route{}, nil, err
	}

	var reasoning *backend.Reasoning
	if raw.Reasoning != nil && raw.Reasoning.Effort != "" {
		if !validReasoningEfforts[raw.Reasoning.Effort] {
			return Route{}, nil, errors.New("reasoning.effort must be one of \"none\", \"minimal\", \"low\", \"medium\", \"high\", \"xhigh\", or \"max\"")
		}
		reasoning = &backend.Reasoning{Effort: raw.Reasoning.Effort}
	}

	return route, &backend.ResponsesRequest{
		Model:     route.Model,
		Stream:    stream,
		Input:     raw.Input,
		Reasoning: reasoning,
		Body:      rewritten,
	}, nil
}

// validateResponsesInput enforces the OpenAI /v1/responses `input` contract:
// the field must be present and either a JSON string or a JSON array whose
// elements are JSON objects. Returns a client-facing error message on failure.
func validateResponsesInput(raw json.RawMessage) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errors.New("request missing required field \"input\"")
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return errors.New("\"input\" must be a string or an array of input items")
		}
		if s == "" {
			return errors.New("request missing required field \"input\"")
		}
		return nil
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return errors.New("\"input\" must be a string or an array of input items")
		}
		for _, item := range items {
			t := bytes.TrimSpace(item)
			if len(t) == 0 || t[0] != '{' {
				return errors.New("\"input\" array elements must be input-item objects")
			}
		}
		return nil
	default:
		return errors.New("\"input\" must be a string or an array of input items")
	}
}
