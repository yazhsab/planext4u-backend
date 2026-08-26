package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yazhsab/planext4u-backend/internal/platform/config"
)

const (
	requestIDHeader = "X-Request-ID"
	maxRequestIDLen = 128
)

// Server is the reusable HTTP service template. Business services add their
// routes to the same hardened middleware and lifecycle foundation.
type Server struct {
	config     config.Config
	httpServer *http.Server
	logger     *slog.Logger
	ready      atomic.Bool
	version    string
}

// New builds a server in the not-ready state.
func New(cfg config.Config, logger *slog.Logger, version string) *Server {
	server := &Server{
		config:  cfg,
		logger:  logger,
		version: version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReadiness)

	server.httpServer = &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           server.middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	return server
}

// Handler exposes the complete HTTP pipeline for contract and integration
// tests without opening a network listener.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Ready reports whether the process is currently eligible for traffic.
func (s *Server) Ready() bool {
	return s.ready.Load()
}

// SetReady controls the readiness gate. Startup and shutdown manage it
// automatically; tests and future dependency checks can explicitly lower it.
func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

// Serve listens until the context is cancelled, then stops accepting traffic
// and drains in-flight requests within the configured timeout.
func (s *Server) Serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.HTTPAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.config.HTTPAddress, err)
	}

	s.SetReady(true)
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- s.httpServer.Serve(listener)
	}()

	select {
	case err := <-serveErrors:
		s.SetReady(false)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		s.SetReady(false)
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			s.config.ShutdownTimeout,
		)
		defer cancel()

		if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}

		err := <-serveErrors
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP during shutdown: %w", err)
		}
		return nil
	}
}

func (s *Server) handleHealth(writer http.ResponseWriter, _ *http.Request) {
	s.writeStatus(writer, http.StatusOK, "ok")
}

func (s *Server) handleReadiness(writer http.ResponseWriter, _ *http.Request) {
	if !s.Ready() {
		s.writeStatus(writer, http.StatusServiceUnavailable, "not_ready")
		return
	}
	s.writeStatus(writer, http.StatusOK, "ready")
}

func (s *Server) writeStatus(writer http.ResponseWriter, statusCode int, status string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(statusCode)
	if err := json.NewEncoder(writer).Encode(map[string]string{
		"status":  status,
		"service": s.config.ServiceName,
		"version": s.version,
	}); err != nil {
		s.logger.Error("write health response", "error", err)
	}
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := validRequestID(request.Header.Get(requestIDHeader))
		if requestID == "" {
			requestID = newRequestID()
		}

		writer.Header().Set(requestIDHeader, requestID)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")

		recorder := &statusRecorder{ResponseWriter: writer, statusCode: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(recorder, request)
		s.logger.Info(
			"http request",
			"request_id", requestID,
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (recorder *statusRecorder) WriteHeader(statusCode int) {
	recorder.statusCode = statusCode
	recorder.ResponseWriter.WriteHeader(statusCode)
}

func validRequestID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxRequestIDLen {
		return ""
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
