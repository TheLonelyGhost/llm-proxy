package sidecar

// This file exposes internal symbols for white-box testing.
// It is compiled only during `go test` runs.

import "net/http"

// HTTPClientForTest returns the HTTP client the Backend sends requests with.
func (b *Backend) HTTPClientForTest() *http.Client { return b.client }
