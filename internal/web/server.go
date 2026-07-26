// Package web exposes the read-only status UI and health endpoints.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/acidghost/hetdns/internal/status"
)

const (
	contentSecurityPolicy = "default-src 'none'; " +
		"style-src 'self'; " +
		"img-src 'self'; " +
		"script-src 'none'; " +
		"connect-src 'none'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'"
	permissionsPolicy = "camera=(), microphone=(), geolocation=(), " +
		"payment=(), usb=()"
)

//go:embed ui/*
var uiFiles embed.FS

// Server is the hardened read-only HTTP surface.
type Server struct {
	http   *http.Server
	store  *status.Store
	page   *template.Template
	logger *slog.Logger
}

// New creates the server. Call Serve only after binding the listener.
func New(address string, store *status.Store, logger *slog.Logger) (*Server, error) {
	page, err := template.New("index.html").Funcs(template.FuncMap{
		"clock": func(value time.Time) string {
			if value.IsZero() {
				return "—"
			}
			return value.Format(time.RFC3339)
		},
		"duration": func(value time.Duration) string {
			if value == 0 {
				return "—"
			}
			return value.Round(time.Millisecond).String()
		},
	}).ParseFS(uiFiles, "ui/index.html")
	if err != nil {
		return nil, err
	}

	server := &Server{
		store:  store,
		page:   page,
		logger: logger,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", server.index)
	mux.HandleFunc("/api/v1/status", server.apiStatus)
	mux.HandleFunc("/healthz", server.health)
	mux.HandleFunc("/readyz", server.ready)

	assets, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		return nil, err
	}
	assetHandler := http.StripPrefix(
		"/assets/",
		http.FileServer(http.FS(assets)),
	)
	mux.Handle("/assets/", assetHandler)

	server.http = &http.Server{
		Addr:              address,
		Handler:           server.middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	return server, nil
}

func (s *Server) Serve(listener net.Listener) error {
	return s.http.Serve(listener)
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error(
					"HTTP handler panic",
					"component", "web",
					"panic", recovered,
					"stack", string(debug.Stack()),
				)
				http.Error(
					response,
					"internal server error",
					http.StatusInternalServerError,
				)
			}
		}()

		response.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Permissions-Policy", permissionsPolicy)

		if strings.HasPrefix(request.URL.Path, "/assets/") {
			response.Header().Set("Cache-Control", "public, max-age=3600")
		}
		if request.Method != http.MethodGet {
			response.Header().Set("Allow", http.MethodGet)
			http.Error(
				response,
				"method not allowed",
				http.StatusMethodNotAllowed,
			)
			return
		}

		next.ServeHTTP(response, request)
	})
}

func (s *Server) index(response http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/" {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")

	if err := s.page.ExecuteTemplate(
		response,
		"index.html",
		s.store.Snapshot(),
	); err != nil {
		s.logger.Error("render status page", "component", "web")
	}
}

func (s *Server) apiStatus(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	encoder := json.NewEncoder(response)
	encoder.SetEscapeHTML(true)

	if err := encoder.Encode(s.store.Snapshot()); err != nil {
		s.logger.Error("encode status response", "component", "web")
	}
}

func (s *Server) health(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")

	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ok\n"))
}

func (s *Server) ready(response http.ResponseWriter, _ *http.Request) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")

	if !s.store.Ready() {
		response.WriteHeader(http.StatusServiceUnavailable)
		_, _ = response.Write([]byte("not ready\n"))
		return
	}

	response.WriteHeader(http.StatusOK)
	_, _ = response.Write([]byte("ready\n"))
}
