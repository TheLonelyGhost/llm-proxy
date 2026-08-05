package router

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParse_Valid(t *testing.T) {
	cases := map[string]Route{
		"copilot/gpt-4o":      {Backend: "copilot", Model: "gpt-4o"},
		"Copilot/gpt-4o":      {Backend: "copilot", Model: "gpt-4o"},
		"copilot/claude-3.5":  {Backend: "copilot", Model: "claude-3.5"},
		"  copilot/gpt-4o   ": {Backend: "copilot", Model: "gpt-4o"},
		"a/b":                 {Backend: "a", Model: "b"},
		"copilot/model-with.dots-and_underscores": {Backend: "copilot", Model: "model-with.dots-and_underscores"},
		"copilot/gpt/4o":         {Backend: "copilot", Model: "gpt/4o"},
		"copilot/org/team/model": {Backend: "copilot", Model: "org/team/model"},
		"copilot//gpt-4o":        {Backend: "copilot", Model: "/gpt-4o"},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := Parse(in)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != want {
				t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
			}
		})
	}
}

func TestParse_Invalid(t *testing.T) {
	bad := []string{
		"",
		" ",
		"copilot",
		"/gpt-4o",
		"copilot/",
		"  /gpt",
		"copilot/ gpt-4o",
		"copilot /gpt-4o",
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			if _, err := Parse(in); !errors.Is(err, ErrInvalidModelID) {
				t.Errorf("Parse(%q) err = %v, want ErrInvalidModelID", in, err)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	if got := Format("copilot", "gpt-4o"); got != "copilot/gpt-4o" {
		t.Errorf("Format = %q", got)
	}
}

func TestParseAndRewrite(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantBackend string
		wantModel   string
		wantStream  bool
		wantErr     bool
	}{
		{
			name:        "rewrites model field",
			body:        `{"model":"copilot/gpt-4o","messages":[]}`,
			wantBackend: "copilot",
			wantModel:   "gpt-4o",
		},
		{
			name:        "preserves stream=true",
			body:        `{"model":"bob/premium","stream":true}`,
			wantBackend: "bob",
			wantModel:   "premium",
			wantStream:  true,
		},
		{
			name:        "preserves stream=false",
			body:        `{"model":"bob/premium","stream":false}`,
			wantBackend: "bob",
			wantModel:   "premium",
			wantStream:  false,
		},
		{
			name:        "preserves unknown fields",
			body:        `{"model":"copilot/gpt-4o","temperature":0.7,"extra":"yes"}`,
			wantBackend: "copilot",
			wantModel:   "gpt-4o",
		},
		{
			name:    "missing model field",
			body:    `{"messages":[]}`,
			wantErr: true,
		},
		{
			name:    "empty model field",
			body:    `{"model":""}`,
			wantErr: true,
		},
		{
			name:    "model without prefix",
			body:    `{"model":"gpt-4o"}`,
			wantErr: true,
		},
		{
			name:    "invalid JSON",
			body:    `not json`,
			wantErr: true,
		},
		{
			name:    "empty body",
			body:    ``,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, stream, rewritten, err := ParseAndRewrite([]byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if route.Backend != tc.wantBackend {
				t.Errorf("Backend = %q, want %q", route.Backend, tc.wantBackend)
			}
			if route.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", route.Model, tc.wantModel)
			}
			if stream != tc.wantStream {
				t.Errorf("stream = %v, want %v", stream, tc.wantStream)
			}
			// Rewritten body must decode and carry the canonical model.
			var out map[string]any
			if err := json.Unmarshal(rewritten, &out); err != nil {
				t.Fatalf("rewritten body is not valid JSON: %v", err)
			}
			if got, _ := out["model"].(string); got != tc.wantModel {
				t.Errorf("rewritten model = %q, want %q", got, tc.wantModel)
			}
		})
	}
}

func FuzzParseModel(f *testing.F) {
	for _, s := range []string{"", "/", "copilot/gpt-4o", "a/b", "x//y", "  c/d ", "copilot/gpt/4o"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		r, err := Parse(s)
		if err != nil {
			return
		}
		// Invariants for successful parses.
		if r.Backend == "" || r.Model == "" {
			t.Fatalf("empty half in %+v from %q", r, s)
		}
		if strings.ContainsRune(r.Backend, '/') {
			t.Fatalf("slash leaked into backend %+v from %q", r, s)
		}
		if r.Backend != strings.ToLower(r.Backend) {
			t.Fatalf("backend not lowercased: %q", r.Backend)
		}
		// Round-trip: format then parse again yields the same Route
		// (modulo whitespace/case folding already applied).
		again, err := Parse(Format(r.Backend, r.Model))
		if err != nil {
			t.Fatalf("round-trip parse failed: %v", err)
		}
		if again != r {
			t.Fatalf("round-trip mismatch: %+v -> %+v", r, again)
		}
	})
}

func FuzzParseAndRewrite(f *testing.F) {
	for _, s := range []string{
		`{"model":"copilot/gpt-4o"}`,
		`{"model":"a/b","stream":true}`,
		`{}`,
		`not json`,
		`{"model":""}`,
		`{"model":"noslash"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		route, stream, rewritten, err := ParseAndRewrite([]byte(s))
		if err != nil {
			return
		}
		// Invariants for successful rewrites.
		if route.Backend == "" || route.Model == "" {
			t.Fatalf("empty route half from %q", s)
		}
		if stream != true && stream != false {
			t.Fatalf("stream has unexpected value")
		}
		var out map[string]any
		if err := json.Unmarshal(rewritten, &out); err != nil {
			t.Fatalf("rewritten body is not valid JSON: %v", err)
		}
		if got, _ := out["model"].(string); got != route.Model {
			t.Fatalf("rewritten model %q != route.Model %q", got, route.Model)
		}
	})
}

func TestParseResponsesRequest(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantBackend string
		wantModel   string
		wantStream  bool
		wantInput   string
		wantEffort  string
		wantErr     string // non-empty → expect an error containing this substring
	}{
		{
			name:        "string input",
			body:        `{"model":"copilot/gpt-4o","input":"hello"}`,
			wantBackend: "copilot",
			wantModel:   "gpt-4o",
			wantInput:   `"hello"`,
		},
		{
			name:        "array input with stream",
			body:        `{"model":"bob/premium","input":[{"type":"message","role":"user","content":"hi"}],"stream":true}`,
			wantBackend: "bob",
			wantModel:   "premium",
			wantStream:  true,
			wantInput:   `[{"type":"message","role":"user","content":"hi"}]`,
		},
		{
			name:        "reasoning effort accepted",
			body:        `{"model":"copilot/o3","input":"think","reasoning":{"effort":"high"}}`,
			wantBackend: "copilot",
			wantModel:   "o3",
			wantInput:   `"think"`,
			wantEffort:  "high",
		},
		{
			name:    "missing model",
			body:    `{"input":"hello"}`,
			wantErr: `"model"`,
		},
		{
			name:    "missing input",
			body:    `{"model":"copilot/gpt-4o"}`,
			wantErr: `"input"`,
		},
		{
			name:    "null input",
			body:    `{"model":"copilot/gpt-4o","input":null}`,
			wantErr: `"input"`,
		},
		{
			name:    "empty string input",
			body:    `{"model":"copilot/gpt-4o","input":""}`,
			wantErr: `"input"`,
		},
		{
			name:    "numeric input rejected",
			body:    `{"model":"copilot/gpt-4o","input":42}`,
			wantErr: "string or an array",
		},
		{
			name:    "array with non-object element",
			body:    `{"model":"copilot/gpt-4o","input":["not an object"]}`,
			wantErr: "input-item objects",
		},
		{
			name:    "invalid reasoning effort",
			body:    `{"model":"copilot/o3","input":"hi","reasoning":{"effort":"turbo"}}`,
			wantErr: "reasoning.effort",
		},
		{
			name:    "model without prefix",
			body:    `{"model":"gpt-4o","input":"hi"}`,
			wantErr: ErrInvalidModelID.Error(),
		},
		{
			name:    "invalid JSON",
			body:    `not json`,
			wantErr: "invalid JSON",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			route, req, err := ParseResponsesRequest([]byte(tc.body))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if route.Backend != tc.wantBackend {
				t.Errorf("Backend = %q, want %q", route.Backend, tc.wantBackend)
			}
			if req.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", req.Model, tc.wantModel)
			}
			if req.Stream != tc.wantStream {
				t.Errorf("Stream = %v, want %v", req.Stream, tc.wantStream)
			}
			if string(req.Input) != tc.wantInput {
				t.Errorf("Input = %s, want %s", req.Input, tc.wantInput)
			}
			if tc.wantEffort != "" {
				if req.Reasoning == nil {
					t.Fatalf("Reasoning is nil, want effort=%q", tc.wantEffort)
				}
				if req.Reasoning.Effort != tc.wantEffort {
					t.Errorf("Reasoning.Effort = %q, want %q", req.Reasoning.Effort, tc.wantEffort)
				}
			} else if req.Reasoning != nil && req.Reasoning.Effort != "" {
				t.Errorf("unexpected Reasoning.Effort = %q", req.Reasoning.Effort)
			}
			// Rewritten body must contain the canonical model.
			var out map[string]any
			if err := json.Unmarshal(req.Body, &out); err != nil {
				t.Fatalf("req.Body is not valid JSON: %v", err)
			}
			if got, _ := out["model"].(string); got != tc.wantModel {
				t.Errorf("rewritten body model = %q, want %q", got, tc.wantModel)
			}
		})
	}
}
