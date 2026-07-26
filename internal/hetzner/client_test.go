package hetzner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
)

func TestReconcileOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, getBody string
		getStatus     int
		record        Record
		want          State
		wantPosts     int32
		wantError     any
	}{
		{
			name:      "unchanged",
			getStatus: http.StatusOK,
			getBody:   `{"rrset":{"records":[{"value":"8.8.8.8"}]}}`,
			record:    testRecord(),
			want:      Unchanged,
		},
		{
			name:      "updated",
			getStatus: http.StatusOK,
			getBody:   `{"rrset":{"records":[{"value":"1.1.1.1"}]}}`,
			record:    testRecord(),
			want:      Updated,
			wantPosts: 1,
		},
		{
			name:      "created",
			getStatus: http.StatusNotFound,
			getBody:   `{}`,
			record:    testRecord(),
			want:      Created,
			wantPosts: 1,
		},
		{
			name:      "missing protected",
			getStatus: http.StatusNotFound,
			getBody:   `{}`,
			record:    recordWithoutCreation(),
			wantError: &MissingError{},
		},
		{
			name:      "multiple protected",
			getStatus: http.StatusOK,
			getBody: `{"rrset":{"records":[` +
				`{"value":"1.1.1.1"},{"value":"9.9.9.9"}` +
				`]}}`,
			record:    testRecord(),
			wantError: &MultiValueError{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var posts atomic.Int32
			handler := http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.Header.Get("Authorization") != "Bearer token" {
					t.Error("missing bearer token")
				}

				response.Header().Set("Content-Type", "application/json")
				if request.Method == http.MethodGet {
					response.WriteHeader(test.getStatus)
					_, _ = response.Write([]byte(test.getBody))
					return
				}

				posts.Add(1)
				response.WriteHeader(http.StatusCreated)
				_, _ = response.Write([]byte(
					`{"action":{"id":12,"status":"success"}}`,
				))
			})
			server := httptest.NewServer(handler)
			defer server.Close()

			client, err := NewWithEndpoint(
				server.URL,
				"token",
				"hetdns/test",
				server.Client(),
			)
			if err != nil {
				t.Fatal(err)
			}

			result, err := client.Reconcile(
				context.Background(),
				test.record,
				netip.MustParseAddr("8.8.8.8"),
			)
			if test.wantError != nil {
				assertReconcileError(t, err, test.wantError)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.State != test.want {
				t.Fatalf("state %q, want %q", result.State, test.want)
			}
			if posts.Load() != test.wantPosts {
				t.Fatalf("posts %d, want %d", posts.Load(), test.wantPosts)
			}
		})
	}
}

func testRecord() Record {
	return Record{
		ID:     "home",
		Zone:   "example.com",
		Name:   "home",
		Type:   "A",
		Create: true,
	}
}

func recordWithoutCreation() Record {
	record := testRecord()
	record.Create = false
	return record
}

func assertReconcileError(t *testing.T, err error, wanted any) {
	t.Helper()

	if err == nil {
		t.Fatal("expected error")
	}

	switch wanted.(type) {
	case *MissingError:
		var target *MissingError
		if !errors.As(err, &target) {
			t.Fatalf("got %T", err)
		}
	case *MultiValueError:
		var target *MultiValueError
		if !errors.As(err, &target) {
			t.Fatalf("got %T", err)
		}
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	t.Parallel()

	var destinationRequests atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		destinationRequests.Add(1)
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(response, request, destination.URL, http.StatusFound)
	}))
	defer origin.Close()

	client, err := NewWithEndpoint(
		origin.URL,
		"supersecret",
		"hetdns/test",
		origin.Client(),
	)
	if err != nil {
		t.Fatal(err)
	}

	_, _ = client.Reconcile(
		context.Background(),
		testRecord(),
		netip.MustParseAddr("8.8.8.8"),
	)
	if destinationRequests.Load() != 0 {
		t.Fatal("authenticated redirect was followed")
	}
}
