package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/acidghost/hetdns/internal/config"
)

const maxSourceOutput = 1024

// HTTP obtains an address with an isolated, SSRF-hardened HTTP client.
type HTTP struct {
	id             string
	family         Family
	url            string
	allowNonPublic bool
	client         *http.Client
	userAgent      string
}

// NewHTTP constructs an HTTP source.
func NewHTTP(id string, spec config.Source, userAgent string) (*HTTP, error) {
	if spec.UserAgent != nil {
		userAgent = *spec.UserAgent
	}
	timeout := spec.Timeout.Duration
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = safeDialer(spec.AllowPrivateNetworks, timeout)
	transport.TLSHandshakeTimeout = min(timeout, 10*time.Second)
	transport.ResponseHeaderTimeout = timeout
	transport.ExpectContinueTimeout = time.Second

	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if via[len(via)-1].URL.Scheme == "https" && request.URL.Scheme != "https" {
			return errors.New("HTTPS redirect downgrade refused")
		}
		setUserAgent(request, userAgent)
		return nil
	}

	return &HTTP{
		id:             id,
		family:         familyOf(spec.Family),
		url:            spec.URL,
		allowNonPublic: spec.AllowNonPublic,
		client:         client,
		userAgent:      userAgent,
	}, nil
}

func safeDialer(
	allowPrivate bool,
	timeout time.Duration,
) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{
		Timeout:   min(timeout, 10*time.Second),
		KeepAlive: 30 * time.Second,
	}
	resolver := net.DefaultResolver
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid source destination")
		}
		addresses, err := resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, errors.New("resolve source destination")
		}

		var lastErr error
		for _, candidate := range addresses {
			candidate = candidate.Unmap()
			if candidate.IsUnspecified() || candidate.IsMulticast() {
				lastErr = errors.New("source destination resolved to an unusable address")
				continue
			}
			if !allowPrivate && !isPublic(candidate) {
				lastErr = errors.New(
					"source destination resolved to a non-public address",
				)
				continue
			}

			destination := net.JoinHostPort(candidate.String(), port)
			connection, dialErr := dialer.DialContext(
				ctx,
				network,
				destination,
			)
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = errors.New("source destination has no usable addresses")
		}

		return nil, lastErr
	}
}

func setUserAgent(request *http.Request, value string) {
	if value == "" {
		// An explicitly empty value suppresses net/http's default User-Agent.
		request.Header["User-Agent"] = []string{""}
		return
	}
	request.Header.Set("User-Agent", value)
}

func (s *HTTP) ID() string     { return s.id }
func (s *HTTP) Family() Family { return s.family }

// CloseIdleConnections releases this source's transport pool.
func (s *HTTP) CloseIdleConnections() { s.client.CloseIdleConnections() }

// Fetch performs one bounded GET and parses its response as one address.
func (s *HTTP) Fetch(ctx context.Context) (netip.Addr, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return netip.Addr{}, errors.New("create source request")
	}
	setUserAgent(request, s.userAgent)
	request.Header.Set("Accept", "text/plain")

	response, err := s.client.Do(request)
	if err != nil {
		return netip.Addr{}, errors.New("HTTP source request failed")
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxSourceOutput))
		return netip.Addr{}, fmt.Errorf("HTTP source returned status %d", response.StatusCode)
	}
	output, err := io.ReadAll(io.LimitReader(response.Body, maxSourceOutput+1))
	if err != nil {
		return netip.Addr{}, errors.New("read HTTP source response")
	}
	if len(output) > maxSourceOutput {
		return netip.Addr{}, errors.New("HTTP source response is too large")
	}

	return ParseAddress(output, s.family, s.allowNonPublic)
}

// SafeURL returns a display-safe URL without credentials, query, or fragment.
func SafeURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "invalid URL"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return strings.TrimSuffix(parsed.String(), "?")
}
