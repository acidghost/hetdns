// Package hetzner implements the minimal Hetzner Cloud zone RRset API.
package hetzner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	APIEndpoint     = "https://api.hetzner.cloud/v1"
	maxResponseBody = 64 << 10
)

// Client is an authenticated, redirect-free Hetzner Cloud client.
type Client struct {
	endpoint  string
	token     string
	userAgent string
	http      *http.Client
	pollEvery time.Duration
	pollLimit time.Duration
}

// New constructs a production client fixed to the official HTTPS API endpoint.
func New(token, userAgent string) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return newClient(APIEndpoint, token, userAgent, &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
}

// NewWithEndpoint constructs a client with an injected endpoint for local tests.
func NewWithEndpoint(endpoint, token, userAgent string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("invalid Hetzner test endpoint")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	copyClient := *httpClient
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return newClient(endpoint, token, userAgent, &copyClient), nil
}

func newClient(endpoint string, token string, userAgent string, httpClient *http.Client) *Client {
	return &Client{
		endpoint:  strings.TrimRight(endpoint, "/"),
		token:     token,
		userAgent: userAgent,
		http:      httpClient,
		pollEvery: time.Second,
		pollLimit: 30 * time.Second,
	}
}

// Reconcile reads the authoritative RRset and creates or safely replaces it as needed.
func (c *Client) Reconcile(ctx context.Context, record Record, desired netip.Addr) (Result, error) {
	observed, found, err := c.getRRSet(ctx, record)
	if err != nil {
		return Result{}, err
	}

	values := make([]string, len(observed))
	for i := range observed {
		values[i] = observed[i].Value
	}

	if !found {
		if !record.Create {
			return Result{Observed: values}, &MissingError{}
		}

		action, err := c.createRRSet(ctx, record, desired)
		if err != nil {
			return Result{Observed: values}, err
		}
		if err := c.awaitAction(ctx, action); err != nil {
			return Result{Observed: values, ActionID: action.ID}, err
		}

		return Result{State: Created, Observed: values, ActionID: action.ID}, nil
	}

	if len(observed) == 1 {
		current, parseErr := netip.ParseAddr(observed[0].Value)
		if parseErr == nil && current.Unmap() == desired.Unmap() {
			return Result{State: Unchanged, Observed: values}, nil
		}
	}
	if len(observed) > 1 && !record.ReplaceAll {
		return Result{Observed: values}, &MultiValueError{Count: len(observed)}
	}

	action, err := c.setRecords(ctx, record, desired)
	if err != nil {
		return Result{Observed: values}, err
	}
	if err := c.awaitAction(ctx, action); err != nil {
		return Result{Observed: values, ActionID: action.ID}, err
	}

	return Result{State: Updated, Observed: values, ActionID: action.ID}, nil
}

func (c *Client) getRRSet(ctx context.Context, record Record) ([]dnsRecord, bool, error) {
	path := rrsetPath(record)
	var response rrsetResponse
	status, err := c.requestJSON(ctx, http.MethodGet, path, nil, &response, true)
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return response.RRSet.Records, true, nil
}

func (c *Client) createRRSet(ctx context.Context, record Record, desired netip.Addr) (action, error) {
	body := struct {
		Name    string      `json:"name"`
		Type    string      `json:"type"`
		TTL     uint32      `json:"ttl,omitempty"`
		Records []dnsRecord `json:"records"`
	}{
		Name:    record.Name,
		Type:    record.Type,
		TTL:     record.TTL,
		Records: []dnsRecord{{Value: desired.String()}},
	}
	path := "/zones/" + url.PathEscape(record.Zone) + "/rrsets"

	return c.mutate(ctx, path, body)
}

func (c *Client) setRecords(ctx context.Context, record Record, desired netip.Addr) (action, error) {
	type bodyType struct {
		Records []dnsRecord `json:"records"`
	}
	body := bodyType{[]dnsRecord{{Value: desired.String()}}}
	path := rrsetPath(record) + "/actions/set_records"
	return c.mutate(ctx, path, body)
}

func rrsetPath(record Record) string {
	return strings.Join([]string{
		"",
		"zones",
		url.PathEscape(record.Zone),
		"rrsets",
		url.PathEscape(record.Name),
		url.PathEscape(record.Type),
	}, "/")
}

func (c *Client) mutate(ctx context.Context, path string, body any) (action, error) {
	var response actionResponse
	_, err := c.requestJSON(ctx, http.MethodPost, path, body, &response, false)
	if err != nil {
		return action{}, err
	}
	if response.Action.ID <= 0 {
		return action{}, errors.New("hetzner API returned an invalid action ID")
	}
	return response.Action, nil
}

func (c *Client) awaitAction(ctx context.Context, current action) error {
	switch current.Status {
	case "success":
		return nil
	case "error":
		return actionFailure(current)
	case "running":
	default:
		return fmt.Errorf(
			"hetzner action %d has unknown status %q",
			current.ID,
			safeText(current.Status, 32),
		)
	}
	pollCtx, cancel := context.WithTimeout(ctx, c.pollLimit)
	defer cancel()
	timer := time.NewTimer(c.pollEvery)
	defer timer.Stop()
	for {
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("waiting for Hetzner action %d: %w", current.ID, pollCtx.Err())
		case <-timer.C:
			var response actionResponse
			path := "/actions/" + strconv.FormatInt(current.ID, 10)
			_, err := c.requestJSON(
				pollCtx,
				http.MethodGet,
				path,
				nil,
				&response,
				true,
			)
			if err != nil {
				return err
			}
			switch response.Action.Status {
			case "success":
				return nil
			case "error":
				return actionFailure(response.Action)
			case "running":
				timer.Reset(c.pollEvery)
			default:
				return fmt.Errorf(
					"hetzner action %d has unknown status %q",
					current.ID,
					safeText(response.Action.Status, 32),
				)
			}
		}
	}
}

func actionFailure(value action) error {
	if value.Error != nil {
		return fmt.Errorf(
			"hetzner action %d failed (%s): %s",
			value.ID,
			safeText(value.Error.Code, 64),
			safeText(value.Error.Message, 256),
		)
	}
	return fmt.Errorf("hetzner action %d failed", value.ID)
}

func (c *Client) requestJSON(
	ctx context.Context,
	method string,
	path string,
	input any,
	output any,
	retry bool,
) (int, error) {
	var encoded []byte
	var err error
	if input != nil {
		encoded, err = json.Marshal(input)
		if err != nil {
			return 0, errors.New("encode Hetzner request")
		}
	}
	attempts := 1
	if retry {
		attempts = 3
	}

	for attempt := 0; attempt < attempts; attempt++ {
		var body io.Reader
		if encoded != nil {
			body = bytes.NewReader(encoded)
		}

		request, requestErr := http.NewRequestWithContext(
			ctx,
			method,
			c.endpoint+path,
			body,
		)
		if requestErr != nil {
			return 0, errors.New("create Hetzner request")
		}
		request.Header.Set("Authorization", "Bearer "+c.token)
		request.Header.Set("Accept", "application/json")
		request.Header.Set("User-Agent", c.userAgent)
		if input != nil {
			request.Header.Set("Content-Type", "application/json")
		}

		response, doErr := c.http.Do(request)
		if doErr != nil {
			if retry && attempt+1 < attempts && ctx.Err() == nil {
				if err := retryPause(ctx, attempt); err != nil {
					return 0, err
				}
				continue
			}
			return 0, errors.New("hetzner API request failed")
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBody+1))
		_ = response.Body.Close()
		if readErr != nil {
			return response.StatusCode, errors.New("read Hetzner API response")
		}
		if len(data) > maxResponseBody {
			return response.StatusCode, errors.New("hetzner API response is too large")
		}
		if response.StatusCode >= 500 && retry && attempt+1 < attempts {
			if err := retryPause(ctx, attempt); err != nil {
				return response.StatusCode, err
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if response.StatusCode == http.StatusNotFound {
				return response.StatusCode, &APIError{Status: response.StatusCode}
			}

			apiErr := decodeAPIError(
				response.StatusCode,
				response.Header.Get("Retry-After"),
				data,
			)
			return response.StatusCode, apiErr
		}
		if output != nil {
			if err := json.Unmarshal(data, output); err != nil {
				return response.StatusCode, errors.New("decode Hetzner API response")
			}
		}
		return response.StatusCode, nil
	}
	return 0, errors.New("hetzner API request failed")
}

func retryPause(ctx context.Context, attempt int) error {
	// The random component is scheduling jitter, not a security primitive.
	delay := time.Duration(100*(1<<attempt)+rand.IntN(100)) * time.Millisecond // #nosec G404
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func decodeAPIError(status int, retryAfter string, data []byte) error {
	result := &APIError{Status: status, RetryAfter: parseRetryAfter(retryAfter)}
	var parsed errorResponse
	if json.Unmarshal(data, &parsed) == nil {
		result.Code = safeText(parsed.Error.Code, 64)
		result.Message = safeText(parsed.Error.Message, 256)
	}
	return result
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	return 0
}

func safeText(value string, limit int) string {
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}
