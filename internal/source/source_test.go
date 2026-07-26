package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/acidghost/hetdns/internal/config"
)

func TestParseAddress(t *testing.T) {
	t.Parallel()

	address, err := ParseAddress([]byte(" 8.8.8.8\n"), IPv4, false)
	if err != nil || address.String() != "8.8.8.8" {
		t.Fatalf("got %v, %v", address, err)
	}

	for _, output := range []string{"8.8.8.8 extra", "127.0.0.1", "::1"} {
		if _, err := ParseAddress([]byte(output), IPv4, false); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}

	if _, err := ParseAddress([]byte("10.0.0.1"), IPv4, true); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPFetchAndLimit(t *testing.T) {
	t.Parallel()

	body := "8.8.8.8\n"
	handler := http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = response.Write([]byte(body))
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	spec := config.Source{
		Family:               "ipv4",
		URL:                  server.URL,
		Timeout:              config.Duration{Duration: time.Second},
		AllowInsecureHTTP:    true,
		AllowPrivateNetworks: true,
	}
	item, err := NewHTTP("test", spec, "hetdns/test")
	if err != nil {
		t.Fatal(err)
	}

	address, err := item.Fetch(context.Background())
	if err != nil || address.String() != "8.8.8.8" {
		t.Fatalf("got %v, %v", address, err)
	}

	body = string(make([]byte, maxSourceOutput+1))
	if _, err := item.Fetch(context.Background()); err == nil {
		t.Fatal("oversized body was accepted")
	}
}

func TestHTTPUserAgent(t *testing.T) {
	t.Parallel()

	custom := "custom-client/1.0"
	tests := []struct {
		name      string
		override  *string
		want      string
		wantField bool
	}{
		{name: "default", want: "hetdns/test", wantField: true},
		{name: "override", override: &custom, want: custom, wantField: true},
		{name: "disabled", override: new(string)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			received := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				received <- request.Header.Clone()
				_, _ = response.Write([]byte("8.8.8.8"))
			}))
			defer server.Close()

			item, err := NewHTTP("test", config.Source{
				Family:               "ipv4",
				URL:                  server.URL,
				Timeout:              config.Duration{Duration: time.Second},
				UserAgent:            test.override,
				AllowInsecureHTTP:    true,
				AllowPrivateNetworks: true,
			}, "hetdns/test")
			if err != nil {
				t.Fatal(err)
			}
			defer item.CloseIdleConnections()
			if _, err := item.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}

			header := <-received
			values, present := header["User-Agent"]
			if present != test.wantField {
				t.Fatalf("User-Agent presence = %t, want %t (%q)", present, test.wantField, values)
			}
			if present && header.Get("User-Agent") != test.want {
				t.Fatalf("User-Agent = %q, want %q", header.Get("User-Agent"), test.want)
			}
		})
	}
}

func TestCommandFetchWithoutShell(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("Unix container command")
	}

	spec := config.Source{
		Family:  "ipv4",
		Argv:    []string{"/bin/echo", "8.8.8.8"},
		Timeout: config.Duration{Duration: time.Second},
	}
	item, err := NewCommand("test", spec)
	if err != nil {
		t.Fatal(err)
	}

	address, err := item.Fetch(context.Background())
	if err != nil || address.String() != "8.8.8.8" {
		t.Fatalf("got %v, %v", address, err)
	}
}
