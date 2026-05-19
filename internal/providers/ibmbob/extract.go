package ibmbob

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ExtractedSecrets is the result of inspecting an installed bob binary or
// bundle. It mirrors the JSON layout the reference Python helper writes.
type ExtractedSecrets struct {
	Version             string `json:"version"`
	BuildTimeHMACSecret string `json:"build_time_hmac_secret"`
}

// markerStart and markerEnd bracket the encrypted secret block embedded in
// every bob bundle. The marker UUID is the same constant the upstream
// CLI uses (jQs in the minified source) and is intentionally a stable build
// artifact - bumping the bob version does not change it.
const (
	markerStart = "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:"
	markerEnd   = " */"
)

// versionRE matches the Bob-Shell version literal that is embedded as a
// User-Agent template in every bob bundle.
var versionRE = regexp.MustCompile(`Bob-Shell/(\d+\.\d+\.\d+)`)

// ExtractErrors callers may want to distinguish for nicer CLI messages.
var (
	// ErrMarkerMissing is returned when the encrypted marker block is
	// absent from the file - usually because the path does not actually
	// point at a bob bundle.
	ErrMarkerMissing = errors.New("encrypted marker block not found")
	// ErrVersionMissing is returned when the Bob-Shell/<version> literal
	// cannot be located.
	ErrVersionMissing = errors.New("Bob-Shell/<version> literal not found")
	// ErrSecretMissing is returned when the marker JSON does not contain
	// a BUILD_TIME_HMAC_SECRET entry.
	ErrSecretMissing = errors.New("BUILD_TIME_HMAC_SECRET entry not found")
)

// Extract reads the bob bundle at path (which may be a symlink) and returns
// the embedded version string and decrypted BUILD_TIME_HMAC_SECRET.
//
// Algorithm, mirroring jQs/QQs/e4n in the minified upstream bundle:
//
//  1. Locate the last "/* fe5f39cc-...:" marker and its closing " */".
//  2. Compute sha256 of the bundle with the entire marker block (including
//     the trailing slash) stripped out. The first 32 bytes of that hex
//     digest become the AES-256-CBC key.
//  3. Parse the marker body as JSON (handling the JS-string-escaped form
//     emitted by minifiers) and AES-256-CBC decrypt the
//     BUILD_TIME_HMAC_SECRET entry using its embedded 16-byte IV.
func Extract(path string) (ExtractedSecrets, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ExtractedSecrets{}, fmt.Errorf("resolve %q: %w", path, err)
	}
	data, err := os.ReadFile(real) //nolint:gosec // operator-supplied path is intended
	if err != nil {
		return ExtractedSecrets{}, fmt.Errorf("read %q: %w", real, err)
	}
	version, err := extractVersion(data)
	if err != nil {
		return ExtractedSecrets{}, err
	}
	key, body, err := keyAndMarkerBody(string(data))
	if err != nil {
		return ExtractedSecrets{}, err
	}
	marker, err := parseMarkerJSON(body)
	if err != nil {
		return ExtractedSecrets{}, fmt.Errorf("parse marker JSON: %w", err)
	}
	entry, ok := marker["BUILD_TIME_HMAC_SECRET"]
	if !ok {
		return ExtractedSecrets{}, ErrSecretMissing
	}
	secret, err := decryptSecretEntry(entry, key)
	if err != nil {
		return ExtractedSecrets{}, fmt.Errorf("decrypt BUILD_TIME_HMAC_SECRET: %w", err)
	}
	return ExtractedSecrets{Version: version, BuildTimeHMACSecret: secret}, nil
}

// extractVersion finds the first Bob-Shell/<x.y.z> literal in the bundle.
func extractVersion(data []byte) (string, error) {
	m := versionRE.FindSubmatch(data)
	if m == nil {
		return "", ErrVersionMissing
	}
	return string(m[1]), nil
}

// keyAndMarkerBody locates the marker block, derives the AES-256-CBC key
// from the marker-stripped bundle, and returns both the key bytes and the
// raw marker body (the substring between markerStart and markerEnd).
func keyAndMarkerBody(text string) ([]byte, string, error) {
	r := strings.LastIndex(text, markerStart)
	if r < 0 {
		return nil, "", ErrMarkerMissing
	}
	rel := strings.Index(text[r:], markerEnd)
	if rel < 0 {
		return nil, "", fmt.Errorf("malformed marker block: no closing %q", markerEnd)
	}
	// n is the index of the final '/' in " */"; sha256 is computed over
	// the bundle with bytes [r..n] removed (i.e. text[:r] + text[n+1:]),
	// matching the upstream stripping rule exactly.
	n := r + rel + 2
	if n+1 > len(text) {
		return nil, "", fmt.Errorf("malformed marker block: truncated tail")
	}
	digest := sha256.New()
	digest.Write([]byte(text[:r]))
	digest.Write([]byte(text[n+1:]))
	hashHex := hex.EncodeToString(digest.Sum(nil))
	key, err := hex.DecodeString(hashHex[:64])
	if err != nil {
		return nil, "", fmt.Errorf("decode key: %w", err)
	}
	bodyStart := r + len(markerStart)
	bodyEnd := r + rel
	if bodyStart > bodyEnd {
		return nil, "", fmt.Errorf("malformed marker block: empty body")
	}
	return key, text[bodyStart:bodyEnd], nil
}

// parseMarkerJSON decodes the marker payload. Pretty-printed bundles store
// raw JSON; minified bundles emit the body as a JS-string-escaped fragment
// (typically un-quoted, e.g. ` {\"BUILD_TIME_HMAC_SECRET\":...}`), which is
// what the reference helper recovers via json.loads(json.loads('"...":')).
func parseMarkerJSON(body string) (map[string]json.RawMessage, error) {
	body = strings.TrimSpace(body)
	body = strings.TrimSuffix(body, "*/")
	body = strings.TrimSpace(body)

	// First: raw JSON object.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err == nil {
		return obj, nil
	}
	// Second: body is itself a JSON string literal (already quoted).
	var inner string
	if err := json.Unmarshal([]byte(body), &inner); err == nil {
		if err := json.Unmarshal([]byte(inner), &obj); err == nil {
			return obj, nil
		}
	}
	// Third (the common case for minified bundles): body is the
	// _contents_ of a JS-string with escape sequences left in place.
	// Wrap in quotes to turn it into a valid JSON string literal, then
	// JSON-decode twice.
	if err := json.Unmarshal([]byte(`"`+body+`"`), &inner); err == nil {
		if err := json.Unmarshal([]byte(inner), &obj); err == nil {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("body is neither a JSON object nor an (un)quoted JS-escaped JSON string")
}

// nodeBuffer mirrors the shape Node serialises a Buffer to: {type: "Buffer", data: [int, ...]}.
type nodeBuffer struct {
	Type string `json:"type"`
	Data []int  `json:"data"`
}

// secretEntry is the layout of a single encrypted secret in the marker JSON.
type secretEntry struct {
	IV        nodeBuffer `json:"iv"`
	Encrypted string     `json:"encrypted"`
}

func decryptSecretEntry(raw json.RawMessage, key []byte) (string, error) {
	var entry secretEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", fmt.Errorf("decode entry: %w", err)
	}
	if len(entry.IV.Data) != aes.BlockSize {
		return "", fmt.Errorf("iv.data length = %d, want %d", len(entry.IV.Data), aes.BlockSize)
	}
	iv := make([]byte, aes.BlockSize)
	for i, v := range entry.IV.Data {
		if v < 0 || v > 255 {
			return "", fmt.Errorf("iv.data[%d] = %d out of byte range", i, v)
		}
		iv[i] = byte(v)
	}
	ciphertext, err := hex.DecodeString(entry.Encrypted)
	if err != nil {
		return "", fmt.Errorf("decode encrypted hex: %w", err)
	}
	plain, err := aesCBCDecrypt(ciphertext, key, iv)
	if err != nil {
		return "", err
	}
	if !isUTF8Safe(plain) {
		return "", fmt.Errorf("decrypted secret is not valid UTF-8")
	}
	return string(plain), nil
}

func aesCBCDecrypt(ciphertext, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a multiple of %d", len(ciphertext), aes.BlockSize)
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	out := make([]byte, len(ciphertext))
	mode.CryptBlocks(out, ciphertext)
	return pkcs7Unpad(out, aes.BlockSize)
}

// pkcs7Unpad validates and removes PKCS#7 padding from in.
func pkcs7Unpad(in []byte, blockSize int) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	pad := int(in[len(in)-1])
	if pad == 0 || pad > blockSize || pad > len(in) {
		return nil, fmt.Errorf("invalid pkcs7 padding length %d", pad)
	}
	for i := len(in) - pad; i < len(in); i++ {
		if int(in[i]) != pad {
			return nil, fmt.Errorf("invalid pkcs7 padding bytes")
		}
	}
	return in[:len(in)-pad], nil
}

// isUTF8Safe is a cheap check that avoids returning binary garbage when an
// extraction silently decrypts to nonsense.
func isUTF8Safe(b []byte) bool {
	for _, c := range string(b) {
		if c == 0xFFFD {
			return false
		}
	}
	return true
}
