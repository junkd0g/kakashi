package kakashi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommandOptionsPreserveExactStatusAndIgnoreBody(t *testing.T) {
	status := 200
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte("not JSON"))
	}))
	defer s.Close()
	c := New(s.URL)
	e := NewEndpoint[struct{}](MethodGet, "/")
	if _, err := Call(context.Background(), c, e, WithExpectedStatus(200), WithDiscardResponse()); err != nil {
		t.Fatal(err)
	}
	status = 201
	_, err := Call(context.Background(), c, e, WithExpectedStatus(200), WithDiscardResponse())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 201 {
		t.Fatalf("status contract lost: %v", err)
	}
}
