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
	"unicode/utf8"
)

// ExtractedSecrets is the result of inspecting an installed bob binary or
// bundle. It mirrors the JSON layout the reference helper writes.
type ExtractedSecrets struct {
	Version             string `json:"version"`
	BuildTimeHMACSecret string `json:"build_time_hmac_secret"`
}

// markerEnd closes every UUID-tagged block comment in the bob bundle.
const markerEnd = " */"

// markerRE matches the opening of any UUID-tagged block-comment payload
// of the form "/* <uuid>:".  The UUID itself is not pinned: the reference
// implementation (read-build-value.mjs) explicitly notes that it "may
// change", so we identify payload blocks by structure rather than by a
// fixed identifier.
var markerRE = regexp.MustCompile(`/\*\s+[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:`)

// versionRE matches the Bob-Shell version literal that is embedded as a
// User-Agent template in every bob bundle.
var versionRE = regexp.MustCompile(`Bob-Shell/(\d+\.\d+\.\d+)`)

// ExtractErrors callers may want to distinguish for nicer CLI messages.
var (
	// ErrMarkerMissing is returned when no UUID-tagged block-comment
	// containing the requested encrypted entry is found in the file.
	ErrMarkerMissing = errors.New("encrypted marker block not found")
	// ErrVersionMissing is returned when the Bob-Shell/<version> literal
	// cannot be located.
	ErrVersionMissing = errors.New("Bob-Shell/<version> literal not found")
)

// Extract reads the bob bundle at path (which may be a symlink) and returns
// the embedded version string and decrypted BUILD_TIME_HMAC_SECRET.
//
// Algorithm mirrors scripts/read-build-value.mjs from pi-dangit-bobby:
//
//  1. Scan all "/* <uuid>: … */" block-comment payloads; keep every one
//     whose JSON (raw or with \" → " unescaping) contains both an
//     "encrypted" field and an "iv" field for the requested key.
//     Use the last such candidate, matching upstream DUn's lastIndexOf.
//  2. Compute SHA-256 over the bundle with the candidate's block stripped
//     out entirely (source[0..start] + source[end..]).  The raw 32-byte
//     digest becomes the AES-256-CBC key.
//  3. AES-256-CBC decrypt the ciphertext.  The IV may be a NodeBuffer
//     ({type:"Buffer",data:[…]}) or a bare integer array.
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
	key, entry, err := findCandidate(string(data), "BUILD_TIME_HMAC_SECRET")
	if err != nil {
		return ExtractedSecrets{}, err
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

// findCandidate scans all UUID-tagged block-comment payloads in text,
// keeps every one that parses as JSON and contains the requested key with
// both "encrypted" and "iv" fields, then uses the last such candidate
// (matching upstream DUn's lastIndexOf behaviour).
//
// It returns the AES-256-CBC key (SHA-256 of the bundle with the candidate
// block stripped) and the raw JSON of the matching secret entry.
func findCandidate(text, name string) ([]byte, json.RawMessage, error) {
	type candidate struct {
		start int
		end   int
		entry json.RawMessage
	}
	var candidates []candidate

	for _, loc := range markerRE.FindAllStringIndex(text, -1) {
		start := loc[0]
		payloadStart := loc[1] // right after the colon
		closeRel := strings.Index(text[payloadStart:], markerEnd)
		if closeRel < 0 {
			continue
		}
		closeAbs := payloadStart + closeRel
		end := closeAbs + len(markerEnd)

		encoded := strings.TrimSpace(text[payloadStart:closeAbs])

		// Two representations to try, mirroring read-build-value.mjs:
		//   1. encoded as-is
		//   2. with JS-string \" escape sequences replaced by literal "
		for _, rep := range []string{encoded, strings.ReplaceAll(encoded, `\"`, `"`)} {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal([]byte(rep), &obj); err != nil {
				continue
			}
			entry, ok := obj[name]
			if !ok {
				continue
			}
			// Entry must have both encrypted and iv fields.
			var check struct {
				Encrypted string          `json:"encrypted"`
				IV        json.RawMessage `json:"iv"`
			}
			if err := json.Unmarshal(entry, &check); err != nil || check.Encrypted == "" || len(check.IV) == 0 {
				continue
			}
			candidates = append(candidates, candidate{start: start, end: end, entry: entry})
			break
		}
	}

	if len(candidates) == 0 {
		return nil, nil, ErrMarkerMissing
	}
	last := candidates[len(candidates)-1]

	// SHA-256 over source[0..start] + source[end..], using the raw digest
	// bytes directly as the 32-byte AES-256 key — matching U5s() in the bundle.
	h := sha256.New()
	h.Write([]byte(text[:last.start]))
	h.Write([]byte(text[last.end:]))
	key := h.Sum(nil) // 32 bytes

	return key, last.entry, nil
}

// secretEntry is the layout of a single encrypted secret in the marker JSON.
// IV may be a NodeBuffer ({type:"Buffer",data:[…]}) or a bare integer array;
// we decode it as raw JSON and resolve the actual bytes in ivBytes().
type secretEntry struct {
	IV        json.RawMessage `json:"iv"`
	Encrypted string          `json:"encrypted"`
}

// ivBytes resolves the IV from either a NodeBuffer shape or a bare []int,
// mirroring Buffer.from(entry.iv.data ?? entry.iv) in read-build-value.mjs.
func ivBytes(raw json.RawMessage) ([]byte, error) {
	// Try NodeBuffer: {"type":"Buffer","data":[…]}
	var nb struct {
		Data []int `json:"data"`
	}
	if err := json.Unmarshal(raw, &nb); err == nil && len(nb.Data) > 0 {
		return intsToBytes(nb.Data)
	}
	// Try bare array: [1,2,3,…]
	var arr []int
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return intsToBytes(arr)
	}
	return nil, fmt.Errorf("iv is neither a NodeBuffer nor a bare integer array")
}

func intsToBytes(ints []int) ([]byte, error) {
	if len(ints) != aes.BlockSize {
		return nil, fmt.Errorf("iv.data length = %d, want %d", len(ints), aes.BlockSize)
	}
	out := make([]byte, aes.BlockSize)
	for i, v := range ints {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("iv.data[%d] = %d out of byte range", i, v)
		}
		out[i] = byte(v)
	}
	return out, nil
}

func decryptSecretEntry(raw json.RawMessage, key []byte) (string, error) {
	var entry secretEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return "", fmt.Errorf("decode entry: %w", err)
	}
	iv, err := ivBytes(entry.IV)
	if err != nil {
		return "", err
	}
	ciphertext, err := hex.DecodeString(entry.Encrypted)
	if err != nil {
		return "", fmt.Errorf("decode encrypted hex: %w", err)
	}
	plain, err := aesCBCDecrypt(ciphertext, key, iv)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(plain) {
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
