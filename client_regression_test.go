package kakashi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func responseClient(status int, body io.ReadCloser, inspect func(*http.Request)) *Client {
	return New("https://example.test/v1", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if inspect != nil {
			inspect(r)
		}
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: body, Request: r}, nil
	})}))
}

// Reading past the prefix represents a stalled or unbounded response tail.
type guardedBody struct {
	prefix     *strings.Reader
	extraReads int
	closed     bool
}

func (b *guardedBody) Read(p []byte) (int, error) {
	if b.prefix.Len() == 0 {
		b.extraReads++
		return 0, errors.New("unexpected read of stalled tail")
	}
	return b.prefix.Read(p)
}

func (b *guardedBody) Close() error { b.closed = true; return nil }

func TestCall_ErrorBodyDoesNotDrainTail(t *testing.T) {
	body := &guardedBody{prefix: strings.NewReader(strings.Repeat("x", 64<<10))}
	_, err := Call(context.Background(), responseClient(500, body, nil), NewEndpoint[struct{}](MethodGet, "/users"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if len(apiErr.Body) != 64<<10 {
		t.Errorf("body length = %d", len(apiErr.Body))
	}
	if body.extraReads != 0 || !body.closed {
		t.Errorf("cleanup: extra reads=%d, closed=%v", body.extraReads, body.closed)
	}
}

func TestCall_InvalidJSONDoesNotDrainTail(t *testing.T) {
	body := &guardedBody{prefix: strings.NewReader("!")}
	_, err := Call(context.Background(), responseClient(200, body, nil), NewEndpoint[struct{}](MethodGet, "/users"))
	if err == nil {
		t.Fatal("expected decode error")
	}
	if body.extraReads != 0 || !body.closed {
		t.Errorf("cleanup: extra reads=%d, closed=%v", body.extraReads, body.closed)
	}
}

func TestCall_JSONResponseValidation(t *testing.T) {
	for _, tc := range []struct {
		name, body          string
		allowEmpty, wantErr bool
	}{
		{"empty", "", false, true},
		{"whitespace", " \n\t", false, true},
		{"garbage", `{"id":1} garbage`, false, true},
		{"second_object", `{"id":1}{"id":2}`, false, true},
		{"second_null", `{"id":1} null`, false, true},
		{"second_array", `{"id":1} []`, false, true},
		{"truncated", `{"id":`, false, true},
		{"valid", `{"id":1}`, false, false},
		{"trailing_whitespace", "{\"id\":1} \n\t", false, false},
		{"allowed_empty", "", true, false},
		{"allowed_whitespace", " \n", true, false},
		{"allow_empty_still_rejects_garbage", `{"id":1} garbage`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts []RequestOption
			if tc.allowEmpty {
				opts = append(opts, WithAllowEmptyResponse())
			}
			c := responseClient(200, io.NopCloser(strings.NewReader(tc.body)), nil)
			_, err := Call(context.Background(), c, NewEndpoint[map[string]int](MethodGet, "/users"), opts...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, want error=%v", err, tc.wantErr)
			}
		})
	}
}

func TestCall_NoContentResponses(t *testing.T) {
	for _, tc := range []struct {
		method Method
		status int
	}{{MethodHead, 200}, {MethodGet, 204}, {MethodPost, 205}} {
		t.Run(string(tc.method)+http.StatusText(tc.status), func(t *testing.T) {
			body := &guardedBody{prefix: strings.NewReader("")}
			got, err := Call(context.Background(), responseClient(tc.status, body, nil), NewEndpoint[int](tc.method, "/users"))
			if err != nil || got != 0 {
				t.Fatalf("got %d, %v", got, err)
			}
			if body.extraReads != 0 || !body.closed {
				t.Errorf("unexpected body handling: %+v", body)
			}
		})
	}
}

func TestCall_EndpointEscaping(t *testing.T) {
	for _, path := range []string{"/users/100%", "/users/%zz", "/users/%2"} {
		t.Run(path, func(t *testing.T) {
			c := responseClient(200, http.NoBody, func(*http.Request) { t.Error("invalid path reached transport") })
			_, err := Call(context.Background(), c, NewEndpoint[struct{}](MethodDelete, path))
			if err == nil {
				t.Fatal("expected invalid path error")
			}
		})
	}
	c := responseClient(200, io.NopCloser(strings.NewReader(`{}`)), func(r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/v1/users/100%25/a%2Fb" {
			t.Errorf("escaped path=%q", got)
		}
	})
	_, err := Call(context.Background(), c, NewEndpoint[struct{}](MethodGet, "/users/100%25/a%2Fb"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithHTTPClient_IsolatesTimeoutAndPreservesConfiguration(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{}
	redirectErr := errors.New("redirect disabled")
	shared := &http.Client{Timeout: time.Minute, Transport: transport, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return redirectErr }}
	first := New("https://example.test", WithHTTPClient(shared))
	second := New("https://example.test", WithHTTPClient(shared), WithTimeout(time.Second))
	if shared.Timeout != time.Minute || first.HTTPClient.Timeout != time.Minute || second.HTTPClient.Timeout != time.Second {
		t.Fatal("timeout leaked between clients")
	}
	if second.HTTPClient == shared || second.HTTPClient == first.HTTPClient {
		t.Fatal("clients share mutable configuration")
	}
	if second.HTTPClient.Transport != transport || second.HTTPClient.Jar != jar || second.HTTPClient.CheckRedirect(nil, nil) != redirectErr {
		t.Fatal("HTTP configuration was not preserved")
	}
	before := New("https://example.test", WithTimeout(time.Second), WithHTTPClient(shared))
	if before.HTTPClient.Timeout != time.Minute {
		t.Fatal("options did not apply in order")
	}
}

func TestCall_ErrorURLRedaction(t *testing.T) {
	const raw = "https://alice:dummy-password@example.test/v1?api_key=dummy-token#private-fragment"
	const safe = "https://example.test/v1/users"
	c := responseClient(500, io.NopCloser(strings.NewReader("oops")), func(r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "alice" || password != "dummy-password" || r.URL.Query().Get("api_key") != "dummy-token" {
			t.Error("redaction changed outgoing authentication")
		}
	})
	c.BaseURL = raw
	_, err := Call(context.Background(), c, NewEndpoint[struct{}](MethodGet, "/users"))
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatal(err)
	}
	if apiErr.URL != safe || err.Error() != "kakashi: "+safe+" returned Internal Server Error" {
		t.Errorf("unsafe error: %+v", apiErr)
	}
	manual := &APIError{URL: raw, Status: "500"}
	if manual.Error() != "kakashi: https://example.test/v1 returned 500" {
		t.Errorf("manual error not redacted: %v", manual)
	}
}

func TestCall_TransportErrorRedactionPreservesCause(t *testing.T) {
	cause := errors.New("connection failed")
	c := New("https://alice:dummy-password@example.test/v1?api_key=dummy-token", WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, cause })}))
	_, err := Call(context.Background(), c, NewEndpoint[struct{}](MethodGet, "/users"))
	var urlErr *url.Error
	if !errors.Is(err, cause) || !errors.As(err, &urlErr) {
		t.Fatalf("lost transport cause: %v", err)
	}
	if urlErr.URL != "https://example.test/v1/users" || strings.Contains(err.Error(), "dummy-") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestCall_QueryOptionsAppendWithoutMutatingInputs(t *testing.T) {
	values := url.Values{"tag": make([]string, 1, 4), "page": {"2"}}
	values["tag"][0] = "first"
	option := WithQuery(values)
	for i := 0; i < 2; i++ {
		c := responseClient(200, io.NopCloser(strings.NewReader(`{}`)), func(r *http.Request) {
			want := url.Values{"tag": {"base", "first", "second"}, "page": {"2"}, "limit": {"50"}}
			if got := r.URL.Query(); !reflect.DeepEqual(got, want) {
				t.Errorf("query=%v, want %v", got, want)
			}
		})
		c.BaseURL += "?tag=base"
		_, err := Call(context.Background(), c, NewEndpoint[struct{}](MethodGet, "/users"), option, WithQuery(url.Values{"tag": {"second"}, "limit": {"50"}}))
		if err != nil {
			t.Fatal(err)
		}
	}
	if values["tag"][:2][1] != "" {
		t.Fatal("query options mutated caller's backing array")
	}
}

func TestCallBody_RawBodyOverridesJSON(t *testing.T) {
	c := responseClient(200, io.NopCloser(strings.NewReader(`{}`)), func(r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "name=Alice" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected raw request: %q, %v, %v", body, r.Header, err)
		}
	})
	_, err := CallBody(context.Background(), c, NewBodyEndpoint[chan int, struct{}](MethodPost, "/users"), make(chan int), WithRawBody(strings.NewReader("name=Alice"), "application/x-www-form-urlencoded"))
	if err != nil {
		t.Fatal(err)
	}
}
