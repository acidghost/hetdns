package hetzner

import (
	"fmt"
	"time"
)

// Record is the provider-facing immutable RRset configuration.
type Record struct {
	ID         string
	Zone       string
	Name       string
	Type       string
	TTL        uint32
	Create     bool
	ReplaceAll bool
}

// State describes the outcome of reconciling an RRset.
type State string

const (
	Unchanged State = "unchanged"
	Updated   State = "updated"
	Created   State = "created"
)

// Result is a successful provider reconciliation result.
type Result struct {
	State    State
	Observed []string
	ActionID int64
}

type rrsetResponse struct {
	RRSet struct {
		ID      string      `json:"id"`
		Name    string      `json:"name"`
		Type    string      `json:"type"`
		TTL     uint32      `json:"ttl"`
		Records []dnsRecord `json:"records"`
	} `json:"rrset"`
}

type dnsRecord struct {
	Value string `json:"value"`
}

type actionResponse struct {
	Action action `json:"action"`
}

type action struct {
	ID     int64     `json:"id"`
	Status string    `json:"status"`
	Error  *apiError `json:"error,omitempty"`
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// APIError is a safely decoded Hetzner API failure.
type APIError struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Code == "" && e.Message == "" {
		return fmt.Sprintf("Hetzner API returned status %d", e.Status)
	}
	return fmt.Sprintf("Hetzner API status %d (%s): %s", e.Status, e.Code, e.Message)
}

// MultiValueError indicates an RRset protected from destructive replacement.
type MultiValueError struct{ Count int }

func (e *MultiValueError) Error() string {
	return fmt.Sprintf("RRset has %d values; set replace_all to allow replacement", e.Count)
}

// MissingError indicates creation was disabled for an absent RRset.
type MissingError struct{}

func (*MissingError) Error() string { return "RRset does not exist and creation is disabled" }
