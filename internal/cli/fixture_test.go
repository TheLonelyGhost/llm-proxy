package cli_test

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// buildFixtureBundle constructs a fake bob.js file the extractor can
// process. Returns the bundle bytes plus the version/secret embedded.
func buildFixtureBundle(t *testing.T) (bundle []byte, version, secret string) {
	t.Helper()
	version = "1.0.4"
	secret = "c102b927-013e-42e4-bc65-16625f35807a"
	prefix := "// fake bob bundle\nBob-Shell/" + version + "\nstuff\n"
	suffix := "\n// end\n"

	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write([]byte(suffix))
	keyHex := hex.EncodeToString(h.Sum(nil))
	key, err := hex.DecodeString(keyHex[:64])
	if err != nil {
		t.Fatal(err)
	}
	iv := []byte("0123456789ABCDEF")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	bs := aes.BlockSize
	pad := bs - (len(secret) % bs)
	padded := make([]byte, len(secret)+pad)
	copy(padded, secret)
	for i := len(secret); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ct, padded)

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
	bundle = []byte(prefix + "/* fe5f39cc-5de4-4123-b4b6-e5fc51deefbd:" + string(body) + " */" + suffix)
	return
}
