// Package server implements the OpenAI-compatible HTTP front door.
//
// Endpoints:
//
//	GET  /healthz                   -> 200 OK liveness probe
//	GET  /v1/models                 -> aggregated, prefix-qualified model list (OpenAI spec)
//	GET  /v1/model/info             -> model info with capabilities, token limits, reasoning effort levels
//	GET  /v1/usage/budget           -> per-backend spend / quota (provider-extension; ibm-bob "Bobcoins" today)
//	POST /v1/chat/completions       -> routed to backend (streaming preserved)
//	POST /v1/completions            -> legacy completions
//	POST /v1/embeddings             -> embeddings
//	POST /v1/responses              -> unified response objects with reasoning effort support
//
// The proxy strips the client's Authorization header before forwarding (the
// upstream uses the backend's own credentials), and rewrites the request
// body's "model" field from "<backend>/<id>" to "<id>".
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/thelonelyghost/llm-proxy/internal/backend"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Server is the proxy HTTP server.
type Server struct {
	addr     string
	registry *backend.Registry
	logger   *slog.Logger
	tracer   trace.Tracer

	httpServer *http.Server
}

// Options configure a Server.
type Options struct {
	Address  string
	Port     int
	Registry *backend.Registry
	Logger   *slog.Logger
	Tracer   trace.Tracer
	// ReadHeaderTimeout caps how long the server will wait for headers.
	ReadHeaderTimeout time.Duration
	// IdleTimeout caps idle keep-alive connections.
	IdleTimeout time.Duration
}

// New constructs a Server. The HTTP listener is not started until Run is
// called.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.ReadHeaderTimeout == 0 {
		opts.ReadHeaderTimeout = 15 * time.Second
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 120 * time.Second
	}
	if opts.Tracer == nil {
		opts.Tracer = tracenoop.NewTracerProvider().Tracer("server")
	}
	addr := net.JoinHostPort(opts.Address, strconv.Itoa(opts.Port))
	s := &Server{
		addr:     addr,
		registry: opts.Registry,
		logger:   logger,
		tracer:   opts.Tracer,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("GET /v1/model/info", s.handleModelInfo)
	mux.HandleFunc("GET /v1/usage/budget", s.handleBudget)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/completions", s.handleCompletion)
	mux.HandleFunc("POST /v1/embeddings", s.handleEmbedding)
	mux.HandleFunc("POST /v1/responses", s.handleResponses)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           withLogging(logger, s.tracer, mux),
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		IdleTimeout:       opts.IdleTimeout,
	}
	return s
}

// Addr returns the bound address.
func (s *Server) Addr() string { return s.addr }

// Run starts the listener and blocks until ctx is cancelled, then performs a
// graceful shutdown with a 10s grace period.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.logger.Info("listening", "addr", ln.Addr().String())
	return s.serve(ctx, ln, 10*time.Second)
}

// Serve is a test helper that runs the server on the supplied listener until
// ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return s.serve(ctx, ln, 5*time.Second)
}

func (s *Server) serve(ctx context.Context, ln net.Listener, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		err := s.httpServer.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		s.logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		return err
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// apiError mirrors the OpenAI error envelope so clients can parse it.
type apiError struct {
	Error apiErrorBody `json:"error"`
}
type apiErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, kind, msg string) {
	writeJSON(w, status, apiError{Error: apiErrorBody{Message: msg, Type: kind}})
}

// withLogging emits a structured access log line per request.
func withLogging(logger *slog.Logger, tracer trace.Tracer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		ctx, span := tracer.Start(r.Context(), "http.request")
		r = r.WithContext(ctx)
		next.ServeHTTP(rw, r)
		span.SetAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.route", r.URL.Path),
			attribute.Int("http.status_code", rw.status),
			attribute.String("net.peer.addr", r.RemoteAddr),
		)
		if rw.status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(rw.status))
		}
		span.End()
		logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer if it supports flushing, so SSE
// works.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter for http.ResponseController.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// copyBody streams an upstream body to the client, flushing on the SSE
// boundaries so streaming completions reach the client promptly.
func copyBody(w http.ResponseWriter, body io.ReadCloser, stream bool) {
	defer func() { _ = body.Close() }()
	if stream {
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
	_, _ = io.Copy(w, body)
}
