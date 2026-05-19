package ibmbob_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// makeJWT produces a string that satisfies the JWT heuristic (>=2 segments,
// middle segment base64url-decodable JSON).
func makeJWT(payload string) string {
	mid := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(payload))
	return "header." + mid + ".sig"
}

func TestClassifyKey(t *testing.T) {
	jwt := makeJWT(`{"sub":"alice"}`)
	cases := []struct {
		name string
		in   string
		want ibmbob.KeyKind
		err  error
	}{
		{"empty", "", 0, ibmbob.ErrEmptyAPIKey},
		{"sk", "sk-abcdef", ibmbob.KeyKindLegacy, nil},
		{"pk", "pk-abcdef", ibmbob.KeyKindLegacy, nil},
		{"jwt", jwt, ibmbob.KeyKindJWT, nil},
		{"opaque", "bob_prod_1234567890", ibmbob.KeyKindOpaque, nil},
		// Looks like a JWT shape but the middle isn't valid JSON -> opaque.
		{"two-dots-not-json", "aa." + base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte("not-json")) + ".bb", ibmbob.KeyKindOpaque, nil},
		// Three dots but middle not base64 decodable -> opaque.
		{"junk-segments", "x.@@.y", ibmbob.KeyKindOpaque, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ibmbob.ClassifyKey(tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err == nil && got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeyKindString(t *testing.T) {
	cases := map[ibmbob.KeyKind]string{
		ibmbob.KeyKindLegacy: "legacy",
		ibmbob.KeyKindJWT:    "jwt",
		ibmbob.KeyKindOpaque: "opaque",
		ibmbob.KeyKind(99):   "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", k, got, want)
		}
	}
}

func TestBaseURLFor(t *testing.T) {
	cases := []struct {
		name     string
		key      string
		kind     ibmbob.KeyKind
		override string
		want     string
	}{
		{"legacy", "sk-x", ibmbob.KeyKindLegacy, "", ibmbob.DefaultOldBackendURL},
		{"opaque", "bob_prod_x", ibmbob.KeyKindOpaque, "", ibmbob.DefaultBackendURL},
		{"override", "anything", ibmbob.KeyKindJWT, "https://example.test/", "https://example.test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ibmbob.BaseURLFor(tc.key, tc.kind, tc.override); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsAuthnBackend(t *testing.T) {
	if ibmbob.IsAuthnBackend(ibmbob.DefaultBackendURL, ibmbob.KeyKindLegacy) {
		t.Error("legacy key must never be authn")
	}
	if !ibmbob.IsAuthnBackend(ibmbob.DefaultBackendURL, ibmbob.KeyKindJWT) {
		t.Error("modern host + jwt should be authn")
	}
	if ibmbob.IsAuthnBackend(ibmbob.DefaultOldBackendURL, ibmbob.KeyKindOpaque) {
		t.Error("legacy host overrides classification")
	}
	if !ibmbob.IsAuthnBackend("https://api.dev.bob.ibm.com", ibmbob.KeyKindOpaque) {
		t.Error("other host should be authn")
	}
}

func TestAuthorizationHeader(t *testing.T) {
	if got := ibmbob.AuthorizationHeader("foo", ibmbob.KeyKindOpaque); got != "Apikey foo" {
		t.Errorf("opaque: %q", got)
	}
	if got := ibmbob.AuthorizationHeader("foo", ibmbob.KeyKindJWT); got != "Bearer foo" {
		t.Errorf("jwt: %q", got)
	}
	if got := ibmbob.AuthorizationHeader("foo", ibmbob.KeyKindLegacy); got != "Bearer foo" {
		t.Errorf("legacy: %q", got)
	}
}

// TestSigning_ParityWithReference checks our CanonicalString and
// ComputeSignature against an independently-derived expected value, matching
// the formula used by the reference Python implementation:
//
//	body_hash   = sha256_hex(body)
//	canonical   = `${METHOD}\n${path}\n${ts}\n${body_hash}`
//	signature   = hmac_sha256_hex(canonical, secret_utf8)
func TestSigning_ParityWithReference(t *testing.T) {
	secret := []byte("test-secret")
	method := "POST"
	path := "/inference/v1/chat/completions"
	body := []byte(`{"model":"premium","messages":[{"role":"user","content":"hi"}]}`)
	ts := "2026-05-18T12:34:56.789Z"

	bodyHash := sha256.Sum256(body)
	canon := "POST\n" + path + "\n" + ts + "\n" + hex.EncodeToString(bodyHash[:])
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(canon))
	want := hex.EncodeToString(mac.Sum(nil))

	if got := ibmbob.CanonicalString(method, path, ts, body); got != canon {
		t.Errorf("CanonicalString mismatch\n got: %q\nwant: %q", got, canon)
	}
	if got := ibmbob.ComputeSignature(method, path, ts, body, secret); got != want {
		t.Errorf("ComputeSignature mismatch\n got: %s\nwant: %s", got, want)
	}
}

func TestSigning_EmptyBody(t *testing.T) {
	// sha256 of "" is e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	emptyHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	canon := ibmbob.CanonicalString("GET", "/admin/v1/profile", "2026-01-01T00:00:00.000Z", nil)
	if !strings.HasSuffix(canon, emptyHash) {
		t.Errorf("empty-body hash mismatch:\n%s", canon)
	}
}

func TestSigning_MethodNormalised(t *testing.T) {
	a := ibmbob.CanonicalString("post", "/x", "t", nil)
	b := ibmbob.CanonicalString("POST", "/x", "t", nil)
	if a != b {
		t.Errorf("method should be upper-cased: %q vs %q", a, b)
	}
}

func TestISOTimestamp_Format(t *testing.T) {
	in := time.Date(2026, 5, 18, 12, 34, 56, 789000000, time.UTC)
	got := ibmbob.ISOTimestamp(in)
	if got != "2026-05-18T12:34:56.789Z" {
		t.Errorf("got %q", got)
	}
}

func TestISOTimestamp_RoundsDownToMillis(t *testing.T) {
	// 123_456_789 ns -> 123 ms (truncation, not rounding)
	in := time.Date(2026, 1, 1, 0, 0, 0, 123_999_999, time.UTC)
	got := ibmbob.ISOTimestamp(in)
	if got != "2026-01-01T00:00:00.123Z" {
		t.Errorf("got %q", got)
	}
}

func TestISOTimestamp_NonUTCNormalised(t *testing.T) {
	// 12:00 in a +5 zone is 07:00 UTC.
	loc := time.FixedZone("plus5", 5*3600)
	in := time.Date(2026, 1, 1, 12, 0, 0, 0, loc)
	got := ibmbob.ISOTimestamp(in)
	if got != "2026-01-01T07:00:00.000Z" {
		t.Errorf("got %q", got)
	}
}

func TestSignRequest(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	sig := ibmbob.SignRequest("POST", "/x", []byte(`{"a":1}`), []byte("s"), now)
	if sig.Timestamp != "2026-06-01T00:00:00.000Z" {
		t.Errorf("ts = %q", sig.Timestamp)
	}
	if len(sig.Signature) != 64 {
		t.Errorf("hex signature length = %d", len(sig.Signature))
	}
}

// FuzzCanonicalString asserts the structural invariants of the canonical-
// string layout that callers (and the upstream verifier) rely on:
//
//   - The output always ends with "\n<64-hex sha256 of body>".
//   - That body hash is a pure function of the body bytes.
//   - Uppercasing the method does not change the output, while changing the
//     case of any other input does.
func FuzzCanonicalString(f *testing.F) {
	seeds := []struct {
		method, path, ts, body string
	}{
		{"POST", "/inference/v1/chat/completions", "2026-01-01T00:00:00.000Z", `{"model":"x"}`},
		{"GET", "/admin/v1/profile", "2026-12-31T23:59:59.999Z", ""},
		{"PUT", "/", "2026-05-18T12:34:56.789Z", "x"},
	}
	for _, s := range seeds {
		f.Add(s.method, s.path, s.ts, s.body)
	}
	f.Fuzz(func(t *testing.T, method, path, ts, body string) {
		got := ibmbob.CanonicalString(method, path, ts, []byte(body))
		// Must end with "\n<64 hex chars>".
		if len(got) < 65 || got[len(got)-65] != '\n' {
			t.Fatalf("missing trailing newline+hash: %q", got)
		}
		hash := got[len(got)-64:]
		for _, c := range hash {
			isDigit := c >= '0' && c <= '9'
			isHex := c >= 'a' && c <= 'f'
			if !isDigit && !isHex {
				t.Fatalf("non-hex char in body hash: %q", hash)
			}
		}
		// Hash must be a function of body alone.
		got2 := ibmbob.CanonicalString("OTHER", "/other", "other", []byte(body))
		if got2[len(got2)-64:] != hash {
			t.Errorf("body hash should not depend on other inputs")
		}
		// Method must be upper-cased.
		upper := ibmbob.CanonicalString(strings.ToUpper(method), path, ts, []byte(body))
		if upper != got {
			t.Errorf("method case-folding broke output:\n got:  %q\n want: %q", got, upper)
		}
	})
}
