package router

import (
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
