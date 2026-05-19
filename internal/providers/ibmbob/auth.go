package ibmbob

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// KeyKind classifies the API key shape, which dictates upstream host,
// authentication scheme, and whether the request body is HMAC-signed.
type KeyKind int

// KeyKind values.
const (
	// KeyKindLegacy is sk-/pk- prefixed keys that target the staging
	// backend with a Bearer header and no HMAC signing.
	KeyKindLegacy KeyKind = iota + 1
	// KeyKindJWT is a key that parses as a JWT and targets the modern
	// backend with a Bearer header and HMAC signing.
	KeyKindJWT
	// KeyKindOpaque is everything else (e.g. bob_prod_<...>) and targets
	// the modern backend with an Apikey header and HMAC signing.
	KeyKindOpaque
)

// String implements fmt.Stringer.
func (k KeyKind) String() string {
	switch k {
	case KeyKindLegacy:
		return "legacy"
	case KeyKindJWT:
		return "jwt"
	case KeyKindOpaque:
		return "opaque"
	default:
		return "unknown"
	}
}

// ErrEmptyAPIKey is returned by ClassifyKey when the input is empty.
var ErrEmptyAPIKey = errors.New("api_key is empty")

// ClassifyKey inspects an API key and returns its handling category.
//
// The classification rules match the reference Python provider exactly so
// that a key that worked there will route to the same upstream here.
func ClassifyKey(apiKey string) (KeyKind, error) {
	if apiKey == "" {
		return 0, ErrEmptyAPIKey
	}
	if strings.HasPrefix(apiKey, "sk-") || strings.HasPrefix(apiKey, "pk-") {
		return KeyKindLegacy, nil
	}
	if looksLikeJWT(apiKey) {
		return KeyKindJWT, nil
	}
	return KeyKindOpaque, nil
}

// looksLikeJWT replicates the reference _is_jwt heuristic: at least two
// dot-separated parts, and the second part is base64url-decodable to valid
// JSON.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return false
	}
	payload := parts[1]
	if pad := len(payload) % 4; pad != 0 {
		payload += strings.Repeat("=", 4-pad)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return false
	}
	var v any
	return json.Unmarshal(decoded, &v) == nil
}

// BaseURLFor returns the upstream base URL to talk to for the given key. An
// explicit override (the api_base HCL field) wins unconditionally.
func BaseURLFor(apiKey string, kind KeyKind, override string) string {
	if override != "" {
		return strings.TrimRight(override, "/")
	}
	if kind == KeyKindLegacy {
		return DefaultOldBackendURL
	}
	return DefaultBackendURL
}

// IsAuthnBackend reports whether baseURL is the modern backend that requires
// HMAC signing. Legacy keys always return false; otherwise any host other
// than the legacy/staging host is treated as the authn backend.
func IsAuthnBackend(baseURL string, kind KeyKind) bool {
	if kind == KeyKindLegacy {
		return false
	}
	return strings.TrimRight(baseURL, "/") != DefaultOldBackendURL
}

// AuthorizationHeader returns the Authorization header value for apiKey.
//
// Opaque keys use the "Apikey <value>" scheme; everything else uses
// "Bearer <value>".
func AuthorizationHeader(apiKey string, kind KeyKind) string {
	if kind == KeyKindOpaque {
		return "Apikey " + apiKey
	}
	return "Bearer " + apiKey
}

// shouldSignURL mirrors the reference Lu.shouldSignUrl: sign whenever
// targeting the modern production host (or the configured baseURL when it
// differs from the legacy/staging host).
func shouldSignURL(url, baseURL string) bool {
	if strings.Contains(url, DefaultBackendURL) {
		return true
	}
	if baseURL == "" {
		return false
	}
	if strings.TrimRight(baseURL, "/") == DefaultOldBackendURL {
		return false
	}
	return strings.Contains(url, baseURL)
}
