package kakashi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestCall(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/users" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id": 1, "name": "Alice"}]`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	GetUsers := NewEndpoint[[]User](MethodGet, "/users")
	client := New(ts.URL)

	users, err := Call(context.Background(), client, GetUsers)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(users) != 1 || users[0].Name != "Alice" {
		t.Errorf("unexpected users result: %+v", users)
	}
}

func TestCall_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer ts.Close()

	client := New(ts.URL)
	GetMissing := NewEndpoint[string](MethodGet, "/missing")

	_, err := Call(context.Background(), client, GetMissing)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", apiErr.StatusCode)
	}
	if string(apiErr.Body) != `{"error":"not found"}` {
		t.Errorf("unexpected error body: %s", apiErr.Body)
	}
}

func TestCallBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/users" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body User
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name != "Bob" {
			t.Errorf("unexpected request body: %+v, error: %v", body, err)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":42,"name":"Bob"}`))
	}))
	defer ts.Close()

	CreateUser := NewBodyEndpoint[User, User](MethodPost, "/users")
	client := New(ts.URL)

	got, err := CallBody(context.Background(), client, CreateUser, User{Name: "Bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 42 || got.Name != "Bob" {
		t.Errorf("unexpected response: %+v", got)
	}
}

func TestCall_Query(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "2" {
			t.Errorf("expected page=2, got %q", r.URL.Query().Get("page"))
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	GetUsers := NewEndpoint[[]User](MethodGet, "/users")
	client := New(ts.URL)

	_, err := Call(context.Background(), client, GetUsers, WithQuery(url.Values{"page": {"2"}}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCall_Headers(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer abc" {
			t.Errorf("expected Authorization header, got %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Trace") != "xyz" {
			t.Errorf("expected X-Trace header, got %q", r.Header.Get("X-Trace"))
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	Get := NewEndpoint[struct{}](MethodGet, "/")
	client := New(ts.URL, WithDefaultHeader("Authorization", "Bearer abc"), WithDefaultHeader("X-Trace", "default"))

	_, err := Call(context.Background(), client, Get, WithHeader("X-Trace", "xyz"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCall_ContextCanceled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	Get := NewEndpoint[struct{}](MethodGet, "/")
	client := New(ts.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Call(ctx, client, Get)
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestCall_URLJoin(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	// Trailing slash on base + leading slash on path must not produce "//".
	client := New(ts.URL + "/v1/")
	Get := NewEndpoint[struct{}](MethodGet, "/users")

	_, err := Call(context.Background(), client, Get)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/v1/users" {
		t.Errorf("expected /v1/users, got %q", gotPath)
	}
}

func TestNewEndpoint_InvalidMethod(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid method, got none")
		}
	}()
	_ = NewEndpoint[struct{}](Method("FOO"), "/x")
}

func TestNewBodyEndpoint_InvalidMethod(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for invalid method, got none")
		}
	}()
	_ = NewBodyEndpoint[User, User](Method(""), "/x")
}
