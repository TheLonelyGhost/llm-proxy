package ibmbob_test

import (
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// FuzzJWTExpiry ensures jwtExpiry never panics on arbitrary input.
func FuzzJWTExpiry(f *testing.F) {
	// Seed corpus: valid JWT shapes and degenerate inputs.
	f.Add("header.e30.sig")    // {"} — minimal valid JWT payload
	f.Add("")                  // empty
	f.Add("nodots")            // no dot separators
	f.Add("a.b")               // two segments, non-base64 payload
	f.Add("a.!!!.c")           // invalid base64
	f.Add("a." + "e30" + ".c") // valid base64 JSON {}
	f.Fuzz(func(t *testing.T, input string) {
		// Must not panic regardless of input.
		_, _ = ibmbob.JWTExpiry(input)
	})
}
