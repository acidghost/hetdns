package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/acidghost/hetdns/internal/buildinfo"
	"github.com/acidghost/hetdns/internal/config"
	"github.com/acidghost/hetdns/internal/status"
)

const webTestConfiguration = `{
  "sources": {
    "s": {
      "type": "http",
      "family": "ipv4",
      "url": "https://example.com"
    }
  },
  "records": {
    "r": {
      "zone": "example.com",
      "name": "home",
      "type": "A",
      "source": "s"
    }
  }
}`

func TestReadOnlyRoutesAndReadiness(t *testing.T) {
	t.Parallel()

	cfg, err := config.Parse([]byte(webTestConfiguration))
	if err != nil {
		t.Fatal(err)
	}

	store := status.New(
		buildinfo.New("test", "secret-commit", "now"),
		cfg,
		2,
	)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New(":0", store, logger)
	if err != nil {
		t.Fatal(err)
	}

	serve := func(method, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		response := httptest.NewRecorder()
		server.http.Handler.ServeHTTP(response, request)
		return response
	}

	response := serve(http.MethodGet, "/readyz")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status %d", response.Code)
	}

	store.SetReady(true)
	response = serve(http.MethodGet, "/readyz")
	if response.Code != http.StatusOK {
		t.Fatalf("ready status %d", response.Code)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}

	response = serve(http.MethodPost, "/healthz")
	if response.Code != http.StatusMethodNotAllowed ||
		response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST status %d", response.Code)
	}

	response = serve(http.MethodGet, "/")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), "home.example.com") {
		t.Fatal("UI did not render record")
	}
}
