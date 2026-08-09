package semver_test

import (
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/semver"
)

// --- Parse error cases ---

func TestParse_Empty(t *testing.T) {
	_, err := semver.Parse("")
	if err == nil {
		t.Fatal("expected error for empty constraint")
	}
}

func TestParse_EmptySegment(t *testing.T) {
	_, err := semver.Parse(">= 1.0.0,")
	if err == nil {
		t.Fatal("expected error for trailing comma")
	}
}

func TestParse_InvalidVersion(t *testing.T) {
	_, err := semver.Parse(">= notaversion")
	if err == nil {
		t.Fatal("expected error for invalid version")
	}
}

// --- Exact match ---

func TestConstraint_ExactMatch(t *testing.T) {
	c := mustParse(t, "1.2.3")
	assertMatches(t, c, "1.2.3", true)
	assertMatches(t, c, "1.2.4", false)
	assertMatches(t, c, "1.2.2", false)
}

func TestConstraint_ExactMatch_WithV(t *testing.T) {
	c := mustParse(t, "= v1.2.3")
	assertMatches(t, c, "1.2.3", true)
	assertMatches(t, c, "v1.2.3", true)
	assertMatches(t, c, "1.2.4", false)
}

// --- Inequality operators ---

func TestConstraint_NotEqual(t *testing.T) {
	c := mustParse(t, "!= 1.2.3")
	assertMatches(t, c, "1.2.3", false)
	assertMatches(t, c, "1.2.4", true)
	assertMatches(t, c, "2.0.0", true)
}

func TestConstraint_GreaterThan(t *testing.T) {
	c := mustParse(t, "> 1.0.0")
	assertMatches(t, c, "1.0.0", false)
	assertMatches(t, c, "1.0.1", true)
	assertMatches(t, c, "2.0.0", true)
}

func TestConstraint_GreaterThanOrEqual(t *testing.T) {
	c := mustParse(t, ">= 1.0.0")
	assertMatches(t, c, "1.0.0", true)
	assertMatches(t, c, "1.0.1", true)
	assertMatches(t, c, "0.9.9", false)
}

func TestConstraint_LessThan(t *testing.T) {
	c := mustParse(t, "< 2.0.0")
	assertMatches(t, c, "1.9.9", true)
	assertMatches(t, c, "2.0.0", false)
	assertMatches(t, c, "2.0.1", false)
}

func TestConstraint_LessThanOrEqual(t *testing.T) {
	c := mustParse(t, "<= 2.0.0")
	assertMatches(t, c, "2.0.0", true)
	assertMatches(t, c, "1.9.9", true)
	assertMatches(t, c, "2.0.1", false)
}

// --- Pessimistic (~>) ---

// ~> 1.0 means >= 1.0.0, < 2.0.0  (Terraform MAJOR bump for 2-part version)
func TestConstraint_Pessimistic_MajorMinor(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	assertMatches(t, c, "1.0.0", true)
	assertMatches(t, c, "1.5.3", true)
	assertMatches(t, c, "1.99.99", true)
	assertMatches(t, c, "2.0.0", false)
	assertMatches(t, c, "0.9.9", false)
}

// ~> 1.0.0 means >= 1.0.0, < 1.1.0
func TestConstraint_Pessimistic_FullVersion(t *testing.T) {
	c := mustParse(t, "~> 1.0.0")
	assertMatches(t, c, "1.0.0", true)
	assertMatches(t, c, "1.0.9", true)
	assertMatches(t, c, "1.1.0", false)
	assertMatches(t, c, "2.0.0", false)
	assertMatches(t, c, "0.9.9", false)
}

// ~> 1.2.3 means >= 1.2.3, < 1.3.0
func TestConstraint_Pessimistic_Patch(t *testing.T) {
	c := mustParse(t, "~> 1.2.3")
	assertMatches(t, c, "1.2.3", true)
	assertMatches(t, c, "1.2.9", true)
	assertMatches(t, c, "1.3.0", false)
	assertMatches(t, c, "1.2.2", false)
}

// --- Compound constraints ---

func TestConstraint_Compound_AndSemantics(t *testing.T) {
	c := mustParse(t, ">= 1.0.0, < 2.0.0")
	assertMatches(t, c, "1.0.0", true)
	assertMatches(t, c, "1.5.0", true)
	assertMatches(t, c, "2.0.0", false)
	assertMatches(t, c, "0.9.9", false)
}

func TestConstraint_Compound_Tight(t *testing.T) {
	c := mustParse(t, ">= 1.2.3, <= 1.2.5")
	assertMatches(t, c, "1.2.3", true)
	assertMatches(t, c, "1.2.4", true)
	assertMatches(t, c, "1.2.5", true)
	assertMatches(t, c, "1.2.6", false)
	assertMatches(t, c, "1.2.2", false)
}

// --- Pre-release filtering ---

func TestConstraint_PrereleasesExcludedByDefault(t *testing.T) {
	c := mustParse(t, ">= 1.0.0")
	// Pre-releases are not matched even when numerically in range.
	assertMatches(t, c, "1.1.0-alpha", false)
	assertMatches(t, c, "1.0.0-rc.1", false)
}

func TestConstraint_ExplicitPrereleaseMatched(t *testing.T) {
	c := mustParse(t, "= 1.0.0-rc.1")
	assertMatches(t, c, "1.0.0-rc.1", true)
	assertMatches(t, c, "1.0.0", false)
}

// --- BestMatch ---

func TestBestMatch_NoCandidates(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	got, err := c.BestMatch(nil)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "" {
		t.Errorf("BestMatch = %q, want empty", got)
	}
}

func TestBestMatch_PicksHighest(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	candidates := []string{"1.0.0", "1.1.0", "1.9.0", "2.0.0", "0.9.0"}
	got, err := c.BestMatch(candidates)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "1.9.0" {
		t.Errorf("BestMatch = %q, want %q", got, "1.9.0")
	}
}

func TestBestMatch_WithVPrefix(t *testing.T) {
	// GitHub releases use "v1.2.3" tags.
	c := mustParse(t, "~> 1.0")
	candidates := []string{"v1.0.0", "v1.5.0", "v2.0.0"}
	got, err := c.BestMatch(candidates)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "1.5.0" {
		t.Errorf("BestMatch = %q, want %q", got, "1.5.0")
	}
}

func TestBestMatch_SkipsPrerelease(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	candidates := []string{"1.0.0", "1.1.0-alpha", "1.0.9"}
	got, err := c.BestMatch(candidates)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "1.0.9" {
		t.Errorf("BestMatch = %q, want %q", got, "1.0.9")
	}
}

func TestBestMatch_NoMatch(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	candidates := []string{"2.0.0", "3.0.0"}
	got, err := c.BestMatch(candidates)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "" {
		t.Errorf("BestMatch = %q, want empty", got)
	}
}

func TestBestMatch_SkipsInvalidTags(t *testing.T) {
	c := mustParse(t, "~> 1.0")
	candidates := []string{"latest", "v1.0.0", "nightly", "v1.2.0"}
	got, err := c.BestMatch(candidates)
	if err != nil {
		t.Fatalf("BestMatch: %v", err)
	}
	if got != "1.2.0" {
		t.Errorf("BestMatch = %q, want %q", got, "1.2.0")
	}
}

// --- IsExact / ExactVersion ---

func TestIsExact(t *testing.T) {
	cases := []struct {
		input   string
		isExact bool
		exact   string
	}{
		{"1.2.3", true, "1.2.3"},
		{"= 1.2.3", true, "1.2.3"},
		{"~> 1.0", false, ""},
		{">= 1.0.0", false, ""},
		{">= 1.0.0, < 2.0.0", false, ""},
	}
	for _, tc := range cases {
		c := mustParse(t, tc.input)
		if got := c.IsExact(); got != tc.isExact {
			t.Errorf("IsExact(%q) = %v, want %v", tc.input, got, tc.isExact)
		}
		if got := c.ExactVersion(); got != tc.exact {
			t.Errorf("ExactVersion(%q) = %q, want %q", tc.input, got, tc.exact)
		}
	}
}

// --- String round-trip ---

func TestConstraint_String(t *testing.T) {
	raw := "~> 1.0"
	c := mustParse(t, raw)
	if c.String() != raw {
		t.Errorf("String() = %q, want %q", c.String(), raw)
	}
}

// --- helpers ---

func mustParse(t *testing.T, s string) semver.Constraint {
	t.Helper()
	c, err := semver.Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return c
}

func assertMatches(t *testing.T, c semver.Constraint, v string, want bool) {
	t.Helper()
	got, err := c.Matches(v)
	if err != nil {
		t.Errorf("Matches(%q): unexpected error: %v", v, err)
		return
	}
	if got != want {
		t.Errorf("Matches(%q) = %v, want %v (constraint: %q)", v, got, want, c)
	}
}
