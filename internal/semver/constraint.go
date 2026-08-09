// Package semver provides Terraform-style version constraint parsing and
// matching against semantic version strings.
//
// Supported operators:
//
//	=  v      exact match
//	!= v      exclusion
//	>  v      strictly greater
//	>= v      greater or equal
//	<  v      strictly less
//	<= v      less or equal
//	~> v      pessimistic constraint (see below)
//
// Pessimistic constraint (~>):
//
//	~> MAJOR.MINOR     means >= MAJOR.MINOR.0, < (MAJOR+1).0.0
//	~> MAJOR.MINOR.PATCH  means >= MAJOR.MINOR.PATCH, < MAJOR.(MINOR+1).0
//
// A constraint string may contain multiple constraints separated by commas,
// all of which must be satisfied simultaneously (logical AND), e.g.:
//
//	">= 1.2.0, < 2.0.0"
//
// Version strings passed to [Matches] need not carry the leading "v"; the
// package normalises them before comparison.
package semver

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	xsemver "golang.org/x/mod/semver"
)

// ErrInvalidConstraint is returned when a constraint string cannot be parsed.
var ErrInvalidConstraint = errors.New("invalid version constraint")

// ErrInvalidVersion is returned when a candidate version string cannot be
// parsed as a semantic version.
var ErrInvalidVersion = errors.New("invalid version")

// Constraint is a parsed, immutable version constraint that can be matched
// against candidate version strings.
type Constraint struct {
	raw   string
	parts []constraintPart
}

type constraintPart struct {
	op  string // one of =, !=, >, >=, <, <=, ~>
	ver string // canonical "vMAJOR.MINOR.PATCH" form
	// For ~> we pre-compute the exclusive upper bound.
	pessimisticCeil string
}

// Parse parses a Terraform-style version constraint string.
// It accepts one or more comma-separated individual constraints.
func Parse(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Constraint{}, fmt.Errorf("%w: empty constraint", ErrInvalidConstraint)
	}
	segments := strings.Split(s, ",")
	parts := make([]constraintPart, 0, len(segments))
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			return Constraint{}, fmt.Errorf("%w: empty segment in %q", ErrInvalidConstraint, s)
		}
		p, err := parsePart(seg)
		if err != nil {
			return Constraint{}, err
		}
		parts = append(parts, p)
	}
	return Constraint{raw: s, parts: parts}, nil
}

// String returns the original constraint string.
func (c Constraint) String() string { return c.raw }

// Matches reports whether the candidate version satisfies all parts of the
// constraint. The candidate may omit the leading "v"; it is added if missing.
// Pre-release versions are never returned as matching unless the constraint
// explicitly names the same pre-release tag.
func (c Constraint) Matches(candidate string) (bool, error) {
	v, err := normalise(candidate)
	if err != nil {
		return false, err
	}
	// Skip pre-release versions unless the constraint explicitly matches them.
	if xsemver.Prerelease(v) != "" {
		if !c.allowsPrerelease(v) {
			return false, nil
		}
	}
	for _, p := range c.parts {
		ok, err := p.matches(v)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// allowsPrerelease returns true when at least one constraint part references
// the exact same pre-release version (i.e. the user explicitly named it).
func (c Constraint) allowsPrerelease(v string) bool {
	for _, p := range c.parts {
		if p.op == "=" && p.ver == v {
			return true
		}
	}
	return false
}

// BestMatch returns the highest version from candidates that satisfies the
// constraint, or an empty string when no candidate matches.
//
// Candidates may omit the leading "v"; they are normalised before comparison.
// Pre-release versions are skipped unless explicitly targeted.
func (c Constraint) BestMatch(candidates []string) (string, error) {
	var best string
	for _, raw := range candidates {
		v, err := normalise(raw)
		if err != nil {
			// Skip unparseable tags silently.
			continue
		}
		ok, err := c.Matches(v)
		if err != nil || !ok {
			continue
		}
		if best == "" || xsemver.Compare(v, best) > 0 {
			best = v
		}
	}
	// Return without the leading "v" to match the input convention of this
	// code-base (version fields do not carry "v").
	if best != "" {
		return strings.TrimPrefix(best, "v"), nil
	}
	return "", nil
}

// IsExact returns true when the constraint is a single exact-version pin
// (no operator, or the "=" operator).
func (c Constraint) IsExact() bool {
	return len(c.parts) == 1 && c.parts[0].op == "="
}

// ExactVersion returns the pinned version string (without leading "v") when
// [IsExact] is true, otherwise returns an empty string.
func (c Constraint) ExactVersion() string {
	if !c.IsExact() {
		return ""
	}
	return strings.TrimPrefix(c.parts[0].ver, "v")
}

// parsePart parses a single constraint such as ">= 1.2.0" or "~> 1.0".
func parsePart(s string) (constraintPart, error) {
	op, verStr := splitOpVer(s)
	ver, err := normalise(verStr)
	if err != nil {
		return constraintPart{}, fmt.Errorf("%w: version %q in %q: %w", ErrInvalidConstraint, verStr, s, err)
	}
	p := constraintPart{op: op, ver: ver}
	if op == "~>" {
		ceil, err := pessimisticCeil(verStr)
		if err != nil {
			return constraintPart{}, fmt.Errorf("%w: %w", ErrInvalidConstraint, err)
		}
		p.pessimisticCeil = ceil
	}
	return p, nil
}

// splitOpVer splits a constraint token like ">= 1.2.0" into (op, ver).
// When no operator is present, op defaults to "=".
func splitOpVer(s string) (op, ver string) {
	s = strings.TrimSpace(s)
	for _, candidate := range []string{"~>", "!=", ">=", "<=", ">", "<", "="} {
		if strings.HasPrefix(s, candidate) {
			return candidate, strings.TrimSpace(s[len(candidate):])
		}
	}
	// No operator — treat as exact match.
	return "=", s
}

// normalise ensures the version string carries a leading "v" and is valid.
func normalise(v string) (string, error) {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	canonical := xsemver.Canonical(v)
	if canonical == "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidVersion, v)
	}
	return canonical, nil
}

// pessimisticCeil computes the exclusive upper bound for "~>" constraints.
//
//	~> 1.0     → ceil = v2.0.0   (bumps MAJOR)
//	~> 1.0.0   → ceil = v1.1.0   (bumps MINOR)
//	~> 1.2.3   → ceil = v1.3.0   (bumps MINOR)
func pessimisticCeil(verStr string) (string, error) {
	// Count the number of dot-separated numeric parts in the raw constraint
	// (before any "v" prefix, without pre-release/build suffix).
	raw := strings.TrimPrefix(strings.TrimSpace(verStr), "v")
	// Strip pre-release / build metadata for counting.
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '+'); i >= 0 {
		raw = raw[:i]
	}
	parts := strings.Split(raw, ".")
	switch len(parts) {
	case 1: // "1" → ~> 1 means >= 1.0.0, < 2.0.0
		major, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid major %q", parts[0])
		}
		return fmt.Sprintf("v%d.0.0", major+1), nil
	case 2: // "1.0" → ~> 1.0 means >= 1.0.0, < 2.0.0 (same as Terraform: bumps MAJOR)
		major, err := strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid major %q", parts[0])
		}
		return fmt.Sprintf("v%d.0.0", major+1), nil
	default: // "1.2.3" → ~> 1.2.3 means >= 1.2.3, < 1.3.0
		minor, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			return "", fmt.Errorf("invalid minor %q", parts[1])
		}
		return fmt.Sprintf("v%s.%d.0", parts[0], minor+1), nil
	}
}

// matches returns whether candidate satisfies this single constraint part.
func (p constraintPart) matches(candidate string) (bool, error) {
	cmp := xsemver.Compare(candidate, p.ver)
	switch p.op {
	case "=":
		return cmp == 0, nil
	case "!=":
		return cmp != 0, nil
	case ">":
		return cmp > 0, nil
	case ">=":
		return cmp >= 0, nil
	case "<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case "~>":
		// >= ver AND < ceil
		if xsemver.Compare(candidate, p.ver) < 0 {
			return false, nil
		}
		return xsemver.Compare(candidate, p.pessimisticCeil) < 0, nil
	default:
		return false, fmt.Errorf("unknown operator %q", p.op)
	}
}
