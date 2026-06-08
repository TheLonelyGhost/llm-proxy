package httplog

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoundTripper_LogsRequestMetadataAtDebug(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, logger, false)}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	log := buf.String()
	if !strings.Contains(log, "upstream request") {
		t.Fatal("expected upstream request log line")
	}
	if !strings.Contains(log, `method=GET`) {
		t.Fatal("expected method=GET in log line")
	}
	if !strings.Contains(log, `status=200`) {
		t.Fatal("expected status=200 in log line")
	}
	if strings.Contains(log, "request_body") {
		t.Fatal("body should not be logged when logBodies=false")
	}
}

func TestRoundTripper_NoLogAtInfoLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, logger, false)}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if buf.Len() > 0 {
		t.Fatal("expected no log output at INFO level")
	}
}

func TestRoundTripper_LogsBodyWhenLogBodiesTrue(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	reqBody := `hello world`
	respBody := `hi there`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		if string(got) != reqBody {
			t.Errorf("got request body %q, want %q", string(got), reqBody)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(respBody))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, logger, true)}

	resp, err := client.Post(srv.URL, "text/plain", strings.NewReader(reqBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	log := buf.String()
	if !strings.Contains(log, "request_body") {
		t.Fatal("expected request_body in log")
	}
	if !strings.Contains(log, reqBody) {
		t.Fatalf("expected request body %q in log, got: %s", reqBody, log)
	}
	if !strings.Contains(log, "response_body") {
		t.Fatal("expected response_body in log")
	}
	if !strings.Contains(log, respBody) {
		t.Fatalf("expected response body %q in log, got: %s", respBody, log)
	}
}

func TestRoundTripper_TruncatesLargeBody(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Generate a response body larger than maxBodyBytes
	largeBody := strings.Repeat("x", maxBodyBytes+100)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(largeBody))
	}))
	defer srv.Close()

	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, logger, true)}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("nil response")
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	log := buf.String()
	if !strings.Contains(log, "response_body_truncated=true") {
		t.Fatal("expected response_body_truncated=true in log")
	}
}

func TestRoundTripper_LogsError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// Point to a server that doesn't exist
	client := &http.Client{Transport: NewRoundTripper(http.DefaultTransport, logger, false)}

	_, err := client.Get("http://127.0.0.1:1/nonexistent") //nolint:bodyclose // request fails, no body to close
	if err == nil {
		t.Fatal("expected error from bad URL")
	}

	log := buf.String()
	if !strings.Contains(log, "upstream request failed") {
		t.Fatal("expected upstream request failed log line")
	}
}

func TestWrapClient_NilTransport(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := &http.Client{}
	WrapClient(client, logger, false)

	if _, ok := client.Transport.(*RoundTripper); !ok {
		t.Fatal("expected client transport to be wrapped")
	}
}

func TestWrapClient_ExistingTransport(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	client := &http.Client{Transport: http.DefaultTransport}
	WrapClient(client, logger, false)

	if _, ok := client.Transport.(*RoundTripper); !ok {
		t.Fatal("expected client transport to be wrapped")
	}
}

func TestNewRoundTripper_NilNext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	rt := NewRoundTripper(nil, logger, false)
	if rt.next == nil {
		t.Fatal("expected next to be set to http.DefaultTransport")
	}
}
