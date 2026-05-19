package ibmbob

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ISOTimestamp formats t in the exact shape Node.js's Date.prototype.toISOString
// emits: "YYYY-MM-DDTHH:MM:SS.mmmZ" - always millisecond precision, always
// the literal "Z" suffix.
func ISOTimestamp(t time.Time) string {
	u := t.UTC()
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02d.%03dZ",
		u.Year(), int(u.Month()), u.Day(),
		u.Hour(), u.Minute(), u.Second(),
		u.Nanosecond()/int(time.Millisecond),
	)
}

// CanonicalString assembles the string that gets HMAC'd. It is exported so
// tests in this package can verify it byte-for-byte against the reference
// Python implementation.
func CanonicalString(method, pathAndQuery, timestamp string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	var sb strings.Builder
	sb.Grow(len(method) + len(pathAndQuery) + len(timestamp) + 64 + 3)
	sb.WriteString(strings.ToUpper(method))
	sb.WriteByte('\n')
	sb.WriteString(pathAndQuery)
	sb.WriteByte('\n')
	sb.WriteString(timestamp)
	sb.WriteByte('\n')
	sb.WriteString(hex.EncodeToString(bodyHash[:]))
	return sb.String()
}

// ComputeSignature returns the lowercase hex HMAC-SHA256 of the canonical
// request string keyed by secret.
func ComputeSignature(method, pathAndQuery, timestamp string, body, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(CanonicalString(method, pathAndQuery, timestamp, body)))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeaders is the (timestamp, signature) pair attached to an
// outbound request that requires HMAC signing.
type SignatureHeaders struct {
	Timestamp string
	Signature string
}

// SignRequest returns the SignatureHeaders to attach to an outbound request.
func SignRequest(method, pathAndQuery string, body, secret []byte, now time.Time) SignatureHeaders {
	ts := ISOTimestamp(now)
	return SignatureHeaders{
		Timestamp: ts,
		Signature: ComputeSignature(method, pathAndQuery, ts, body, secret),
	}
}
