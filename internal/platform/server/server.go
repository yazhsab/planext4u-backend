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
	"github.com/yazhsab/planext4u-backend/internal/platform/telemetry"
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
	readiness  func(context.Context) error
	version    string
}

type Option func(*serverOptions)

type serverOptions struct {
	telemetry         *telemetry.Telemetry
	application       http.Handler
	applicationRoutes telemetry.RouteResolver
	readiness         func(context.Context) error
}

func WithTelemetry(value *telemetry.Telemetry) Option {
	return func(options *serverOptions) { options.telemetry = value }
}

func WithApplication(handler http.Handler, routes telemetry.RouteResolver) Option {
	return func(options *serverOptions) {
		options.application = handler
		options.applicationRoutes = routes
	}
}

func WithReadiness(check func(context.Context) error) Option {
	return func(options *serverOptions) { options.readiness = check }
}

// New builds a server in the not-ready state.
func New(cfg config.Config, logger *slog.Logger, version string, optionValues ...Option) *Server {
	options := serverOptions{}
	for _, option := range optionValues {
		option(&options)
	}
	server := &Server{
		config:    cfg,
		logger:    logger,
		readiness: options.readiness,
		version:   version,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReadiness)
	if options.application != nil {
		mux.Handle("/", options.application)
	}

	var handler http.Handler = server.middleware(mux)
	if options.telemetry != nil {
		handler = options.telemetry.HTTPMiddleware(func(request *http.Request) string {
			switch request.URL.Path {
			case "/healthz", "/readyz":
				return request.URL.Path
			default:
				if options.applicationRoutes != nil {
					return options.applicationRoutes(request)
				}
				return "unmatched"
			}
		})(handler)
	}
	server.httpServer = &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           handler,
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

func (s *Server) handleReadiness(writer http.ResponseWriter, request *http.Request) {
	if !s.Ready() {
		s.writeStatus(writer, http.StatusServiceUnavailable, "not_ready")
		return
	}
	if s.readiness != nil {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if err := s.readiness(ctx); err != nil {
			s.writeStatus(writer, http.StatusServiceUnavailable, "dependency_unavailable")
			return
		}
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
		traceID, spanID := telemetry.TraceContext(request.Context())
		s.logger.Info(
			"http request",
			"request_id", requestID,
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.statusCode,
			"duration_ms", time.Since(started).Milliseconds(),
			"trace_id", traceID,
			"span_id", spanID,
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
