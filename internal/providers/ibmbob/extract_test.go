package ibmbob_test

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
	"runtime"
	"strings"
	"testing"

	"github.com/thelonelyghost/llm-proxy/internal/providers/ibmbob"
)

// synthBundle constructs a fake bob bundle that the extractor can decrypt.
//
// We need to bootstrap a chicken-and-egg problem: the AES key is derived
// from the bundle text after the marker block is stripped, but the marker
// must be embedded with a ciphertext that decrypts under exactly that key.
//
// The trick: the marker stripping yields prefix+suffix, so the key depends
// only on those two halves, not on the marker body itself. So we:
//
//  1. Pick prefix and suffix.
//  2. Compute the key as SHA-256(prefix + suffix) — raw bytes, no hex step.
//  3. Encrypt the plaintext secret with that key under a chosen IV.
//  4. Build the marker JSON around the resulting ciphertext.
//  5. Concatenate prefix + marker + suffix and write to disk.
//
// The UUID used in the marker is arbitrary; the extractor matches any
// UUID-tagged block comment via a regex.
func synthBundle(t *testing.T, version, plaintext string) (path string, prefix, suffix string) {
	t.Helper()
	prefix = "// fake bob bundle\nBob-Shell/" + version + "\nsome other content here\n"
	suffix = "\n// trailing content\n"

	// Derive the same key the extractor will compute: raw SHA-256 digest.
	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write([]byte(suffix))
	key := h.Sum(nil) // 32 bytes

	iv := []byte("0123456789ABCDEF") // exactly 16 bytes
	ciphertext, err := aesCBCEncrypt([]byte(plaintext), key, iv)
	if err != nil {
		t.Fatal(err)
	}

	ivData := make([]int, len(iv))
	for i, b := range iv {
		ivData[i] = int(b)
	}
	marker := map[string]any{
		"BUILD_TIME_HMAC_SECRET": map[string]any{
			"iv":        map[string]any{"type": "Buffer", "data": ivData},
			"encrypted": hex.EncodeToString(ciphertext),
		},
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	// The entire "/* <uuid>:<body> */" span is stripped when computing the
	// key, so inserting it between prefix and suffix preserves prefix+suffix
	// as the keying material — matching step 2 above.
	bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix

	path = filepath.Join(t.TempDir(), "bob.js")
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, prefix, suffix
}

// aesCBCEncrypt is a tiny helper - the production code only ever needs to
// decrypt, but constructing the fixture demands its mirror.
func aesCBCEncrypt(plain, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// PKCS#7 pad.
	bs := aes.BlockSize
	padLen := bs - (len(plain) % bs)
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func TestExtract_RoundTrip(t *testing.T) {
	wantSecret := "c102b927-013e-42e4-bc65-16625f35807a"
	wantVersion := "1.0.4"
	path, _, _ := synthBundle(t, wantVersion, wantSecret)

	got, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.Version != wantVersion {
		t.Errorf("Version = %q, want %q", got.Version, wantVersion)
	}
	if got.BuildTimeHMACSecret != wantSecret {
		t.Errorf("Secret = %q, want %q", got.BuildTimeHMACSecret, wantSecret)
	}
}

func TestExtract_FollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks are awkward on windows runners")
	}
	path, _, _ := synthBundle(t, "1.0.0", "secret")
	link := filepath.Join(t.TempDir(), "bob")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	got, err := ibmbob.Extract(link)
	if err != nil {
		t.Fatal(err)
	}
	if got.BuildTimeHMACSecret != "secret" {
		t.Errorf("secret = %q", got.BuildTimeHMACSecret)
	}
}

func TestExtract_PicksLastMarker(t *testing.T) {
	// Two marker blocks - extractor must pick the last one. We do this
	// by synthesising a valid bundle, then prepending a decoy marker
	// containing nonsense ciphertext. The real key derivation will treat
	// the decoy as part of the "prefix" (since we strip only the LAST
	// marker), so we must regenerate the bundle accordingly.
	wantSecret := "real-secret"
	wantVersion := "9.9.9"
	// Construct prefix to include a decoy marker block (different UUID
	// to confirm the regex matches any UUID, not just the hardcoded one).
	decoy := "/* aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:" + `{"bogus":1}` + " */"
	prefix := "Bob-Shell/" + wantVersion + "\n" + decoy + "\nmiddle\n"
	suffix := "\nend\n"

	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write([]byte(suffix))
	key := h.Sum(nil)
	iv := []byte("ABCDEFGHIJKLMNOP")
	ct, err := aesCBCEncrypt([]byte(wantSecret), key, iv)
	if err != nil {
		t.Fatal(err)
	}
	ivData := make([]int, len(iv))
	for i, b := range iv {
		ivData[i] = int(b)
	}
	marker := map[string]any{
		"BUILD_TIME_HMAC_SECRET": map[string]any{
			"iv":        map[string]any{"type": "Buffer", "data": ivData},
			"encrypted": hex.EncodeToString(ct),
		},
	}
	body, _ := json.Marshal(marker)
	bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix
	path := filepath.Join(t.TempDir(), "bob.js")
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.BuildTimeHMACSecret != wantSecret {
		t.Errorf("secret = %q, want %q", got.BuildTimeHMACSecret, wantSecret)
	}
}

func TestExtract_MinifiedJSStringEscaped(t *testing.T) {
	// Bundlers emit the marker JSON with \" instead of " (the payload is
	// inside a JS string literal in the minified source). The reference
	// implementation recovers it via a simple replace(/\\"/g, '"');
	// verify we do the same.
	wantSecret := "esc-secret"
	prefix := "Bob-Shell/1.2.3\nstuff\n"
	suffix := "\nmore\n"

	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write([]byte(suffix))
	key := h.Sum(nil)
	iv := []byte("0000000000000000")
	ct, err := aesCBCEncrypt([]byte(wantSecret), key, iv)
	if err != nil {
		t.Fatal(err)
	}
	ivData := make([]int, len(iv))
	for i, b := range iv {
		ivData[i] = int(b)
	}
	inner := map[string]any{
		"BUILD_TIME_HMAC_SECRET": map[string]any{
			"iv":        map[string]any{"type": "Buffer", "data": ivData},
			"encrypted": hex.EncodeToString(ct),
		},
	}
	innerJSON, _ := json.Marshal(inner)
	// Produce the JS-string-escaped form: replace " with \" as a bundler would.
	escapedBody := strings.ReplaceAll(string(innerJSON), `"`, `\"`)
	bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + escapedBody + " */" + suffix

	path := filepath.Join(t.TempDir(), "bob.js")
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.BuildTimeHMACSecret != wantSecret {
		t.Errorf("secret = %q, want %q", got.BuildTimeHMACSecret, wantSecret)
	}
}

func TestExtract_BareArrayIV(t *testing.T) {
	// The reference implementation accepts a bare integer array for the IV
	// (Buffer.from(entry.iv.data ?? entry.iv)). Verify we handle that shape.
	wantSecret := "bare-iv-secret"
	prefix := "Bob-Shell/2.0.0\ncontent\n"
	suffix := "\nend\n"

	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write([]byte(suffix))
	key := h.Sum(nil)
	iv := []byte("ABCDEFGHIJKLMNOP")
	ct, err := aesCBCEncrypt([]byte(wantSecret), key, iv)
	if err != nil {
		t.Fatal(err)
	}
	// IV as a bare []int, no "type"/"data" wrapper.
	ivData := make([]int, len(iv))
	for i, b := range iv {
		ivData[i] = int(b)
	}
	entry := map[string]any{
		"BUILD_TIME_HMAC_SECRET": map[string]any{
			"iv":        ivData, // bare array, not a NodeBuffer
			"encrypted": hex.EncodeToString(ct),
		},
	}
	body, _ := json.Marshal(entry)
	bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix
	path := filepath.Join(t.TempDir(), "bob.js")
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.BuildTimeHMACSecret != wantSecret {
		t.Errorf("secret = %q, want %q", got.BuildTimeHMACSecret, wantSecret)
	}
}

func TestExtract_Errors(t *testing.T) {
	t.Run("nonexistent path", func(t *testing.T) {
		_, err := ibmbob.Extract(filepath.Join(t.TempDir(), "missing"))
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("missing version", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte("no version literal here"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if !errors.Is(err, ibmbob.ErrVersionMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing marker", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte("Bob-Shell/1.0.0\nbut no marker"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if !errors.Is(err, ibmbob.ErrMarkerMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unterminated marker", func(t *testing.T) {
		// A UUID block comment with no closing " */" is skipped; because no
		// valid candidate is found the error is ErrMarkerMissing.
		p := filepath.Join(t.TempDir(), "bob.js")
		body := "Bob-Shell/1.0.0\n/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:{}"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if !errors.Is(err, ibmbob.ErrMarkerMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("marker without secret entry", func(t *testing.T) {
		// A block comment whose JSON parses but has no BUILD_TIME_HMAC_SECRET
		// entry with both encrypted+iv is treated as a non-candidate; with no
		// other candidates, ErrMarkerMissing is returned.
		prefix := "Bob-Shell/1.0.0\n"
		suffix := "\n"
		bundle := prefix + `/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:{"other_secret":{}} */` + suffix
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if !errors.Is(err, ibmbob.ErrMarkerMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("malformed marker JSON", func(t *testing.T) {
		// A UUID block comment whose payload is not valid JSON (in either
		// representation) is skipped; no valid candidate → ErrMarkerMissing.
		prefix := "Bob-Shell/1.0.0\n"
		suffix := "\n"
		bundle := prefix + `/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:not-json */` + suffix
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if !errors.Is(err, ibmbob.ErrMarkerMissing) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad iv length", func(t *testing.T) {
		prefix := "Bob-Shell/1.0.0\n"
		suffix := "\n"
		entry := map[string]any{
			"BUILD_TIME_HMAC_SECRET": map[string]any{
				"iv":        map[string]any{"type": "Buffer", "data": []int{1, 2, 3}},
				"encrypted": "00",
			},
		}
		body, _ := json.Marshal(entry)
		bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if err == nil || !strings.Contains(err.Error(), "iv.data length") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("bad ciphertext length", func(t *testing.T) {
		prefix := "Bob-Shell/1.0.0\n"
		suffix := "\n"
		entry := map[string]any{
			"BUILD_TIME_HMAC_SECRET": map[string]any{
				"iv":        map[string]any{"type": "Buffer", "data": ivAllZeros()},
				"encrypted": hex.EncodeToString([]byte("not16bytes")),
			},
		}
		body, _ := json.Marshal(entry)
		bundle := prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := ibmbob.Extract(p)
		if err == nil || !strings.Contains(err.Error(), "not a multiple of") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong key -> bad padding", func(t *testing.T) {
		// Encrypt with one prefix, but write the bundle with a DIFFERENT
		// prefix length so the extractor derives the wrong key.
		// Simplest path: encrypt with our prefix, then append a stray byte
		// to the suffix after building the marker, so the recomputed
		// hash differs.
		wantPrefix := "Bob-Shell/1.0.0\nA\n"
		wantSuffix := "\nZ\n"
		h := sha256.New()
		h.Write([]byte(wantPrefix))
		h.Write([]byte(wantSuffix))
		key := h.Sum(nil)
		iv := []byte("0000000000000000")
		ct, err := aesCBCEncrypt([]byte("secret"), key, iv)
		if err != nil {
			t.Fatal(err)
		}
		ivData := make([]int, len(iv))
		for i, b := range iv {
			ivData[i] = int(b)
		}
		entry := map[string]any{
			"BUILD_TIME_HMAC_SECRET": map[string]any{
				"iv":        map[string]any{"type": "Buffer", "data": ivData},
				"encrypted": hex.EncodeToString(ct),
			},
		}
		body, _ := json.Marshal(entry)
		// Build bundle with EXTRA bytes in the suffix, breaking the key.
		bundle := wantPrefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + wantSuffix + "EXTRA"
		p := filepath.Join(t.TempDir(), "bob.js")
		if err := os.WriteFile(p, []byte(bundle), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = ibmbob.Extract(p)
		// Wrong key produces random-looking plaintext - either bad
		// padding or invalid UTF-8.
		if err == nil {
			t.Fatal("expected error for wrong key")
		}
		if !strings.Contains(err.Error(), "padding") && !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("expected padding/utf8 error, got %v", err)
		}
	})
}

func ivAllZeros() []int {
	out := make([]int, 16)
	return out
}

// TestExtract_Determinism guarantees that two calls on the same file return
// the same bytes (the algorithm is content-addressed, so this should hold
// trivially - but a regression here would be a nasty silent break).
func TestExtract_Determinism(t *testing.T) {
	path, _, _ := synthBundle(t, "1.0.4", "deterministic-secret")
	a, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ibmbob.Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("nondeterministic extract: %+v vs %+v", a, b)
	}
	// Also: the secret must round-trip through JSON unchanged.
	js, _ := json.Marshal(a)
	var c ibmbob.ExtractedSecrets
	if err := json.Unmarshal(js, &c); err != nil {
		t.Fatal(err)
	}
	if c != a {
		t.Errorf("json round-trip mismatch")
	}
	// Smoke-check field tags so the published JSON layout matches the
	// reference helper byte-for-byte (no surprise camelCase, etc.).
	if !strings.Contains(string(js), `"build_time_hmac_secret"`) || !strings.Contains(string(js), `"version"`) {
		t.Errorf("unexpected JSON shape: %s", string(js))
	}
	_ = fmt.Sprintf // keep fmt import in case test additions need it
}
