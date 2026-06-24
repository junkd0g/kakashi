// Package kakashi provides a small, strongly-typed HTTP client.
//
// Endpoints are declared once with their response (and optional request)
// types, then executed against a Client:
//
//	var GetUser = kakashi.NewEndpoint[User](kakashi.MethodGet, "/users/1")
//	user, err := kakashi.Call(ctx, client, GetUser)
//
//	var CreateUser = kakashi.NewBodyEndpoint[User, User](kakashi.MethodPost, "/users")
//	created, err := kakashi.CallBody(ctx, client, CreateUser, User{Name: "Alice"})
package kakashi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Method represents an HTTP method. Use the package-level constants;
// arbitrary strings are rejected at endpoint construction.
type Method string

const (
	MethodGet     Method = http.MethodGet
	MethodHead    Method = http.MethodHead
	MethodPost    Method = http.MethodPost
	MethodPut     Method = http.MethodPut
	MethodDelete  Method = http.MethodDelete
	MethodPatch   Method = http.MethodPatch
	MethodOptions Method = http.MethodOptions
)

// validMethods is the closed set accepted by NewEndpoint / NewBodyEndpoint.
var validMethods = map[Method]struct{}{
	MethodGet: {}, MethodHead: {}, MethodPost: {}, MethodPut: {},
	MethodDelete: {}, MethodPatch: {}, MethodOptions: {},
}

func mustValidMethod(m Method) {
	if _, ok := validMethods[m]; !ok {
		panic(fmt.Sprintf("kakashi: invalid HTTP method %q", string(m)))
	}
}

// DefaultTimeout is the timeout applied to the HTTP client when no
// WithHTTPClient / WithTimeout option is supplied.
const DefaultTimeout = 10 * time.Second

// maxErrorBody caps how much of a non-2xx response body is read into the
// returned APIError, preventing pathological servers from blowing up logs.
const maxErrorBody = 64 << 10

// APIError is returned when the server responds with a non-2xx status code.
// Callers can use errors.As to extract it and branch on StatusCode.
type APIError struct {
	StatusCode int
	Status     string
	URL        string
	Body       []byte
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kakashi: %s returned %s", e.URL, e.Status)
}

// Endpoint is a body-less HTTP endpoint that returns a value of type Resp.
type Endpoint[Resp any] struct {
	Method Method
	Path   string
}

// BodyEndpoint is an HTTP endpoint that accepts a request body of type Req
// and returns a value of type Resp.
type BodyEndpoint[Req, Resp any] struct {
	Method Method
	Path   string
}

// NewEndpoint creates an endpoint with no request body. Panics if method is
// not one of the package-level constants (caught at package init time when
// endpoints are declared as package vars).
func NewEndpoint[Resp any](method Method, path string) Endpoint[Resp] {
	mustValidMethod(method)
	return Endpoint[Resp]{Method: method, Path: path}
}

// NewBodyEndpoint creates an endpoint that sends a typed request body.
// Panics if method is not one of the package-level constants.
func NewBodyEndpoint[Req, Resp any](method Method, path string) BodyEndpoint[Req, Resp] {
	mustValidMethod(method)
	return BodyEndpoint[Req, Resp]{Method: method, Path: path}
}

// Client is an HTTP client bound to a base URL.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	headers    http.Header
}

// Option configures a Client during construction.
type Option func(*Client)

// WithHTTPClient replaces the default *http.Client. Use this to control
// transport, redirect policy, cookie jar, etc.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.HTTPClient = h
		}
	}
}

// WithTimeout sets the request timeout on the underlying *http.Client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		if c.HTTPClient == nil {
			c.HTTPClient = &http.Client{}
		}
		c.HTTPClient.Timeout = d
	}
}

// WithDefaultHeader registers a header that is applied to every request.
// Per-request headers (set via WithHeader) override these.
func WithDefaultHeader(key, value string) Option {
	return func(c *Client) {
		if c.headers == nil {
			c.headers = http.Header{}
		}
		c.headers.Set(key, value)
	}
}

// New creates a new Client.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: DefaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RequestOption configures a single request.
type RequestOption func(*requestConfig)

type requestConfig struct {
	headers http.Header
	query   url.Values
	rawBody io.Reader
	rawCT   string
}

// WithHeader sets a header on a single request, overriding any client default.
func WithHeader(key, value string) RequestOption {
	return func(c *requestConfig) {
		if c.headers == nil {
			c.headers = http.Header{}
		}
		c.headers.Set(key, value)
	}
}

// WithQuery appends query parameters to the request URL.
func WithQuery(values url.Values) RequestOption {
	return func(c *requestConfig) { c.query = values }
}

// WithRawBody sends the given reader as the request body instead of a
// JSON-encoded value. Only honored when calling on an Endpoint (body-less)
// or in place of the JSON body on a BodyEndpoint.
func WithRawBody(r io.Reader, contentType string) RequestOption {
	return func(c *requestConfig) {
		c.rawBody = r
		c.rawCT = contentType
	}
}

// Call executes a body-less endpoint against the given client.
func Call[Resp any](ctx context.Context, c *Client, e Endpoint[Resp], opts ...RequestOption) (Resp, error) {
	return doRequest[Resp](ctx, c, e.Method, e.Path, nil, false, opts)
}

// CallBody executes an endpoint that sends a typed JSON body.
func CallBody[Req, Resp any](ctx context.Context, c *Client, e BodyEndpoint[Req, Resp], body Req, opts ...RequestOption) (Resp, error) {
	return doRequest[Resp](ctx, c, e.Method, e.Path, body, true, opts)
}

func doRequest[Resp any](
	ctx context.Context,
	c *Client,
	method Method,
	path string,
	body any,
	hasJSONBody bool,
	opts []RequestOption,
) (Resp, error) {
	var zero Resp

	if c == nil || c.HTTPClient == nil {
		return zero, errors.New("kakashi: client is nil or has no HTTPClient")
	}

	cfg := requestConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	if c.BaseURL == "" {
		return zero, errors.New("kakashi: client has no BaseURL")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return zero, fmt.Errorf("kakashi: invalid base URL: %w", err)
	}
	if path != "" {
		u = u.JoinPath(path)
	}
	if len(cfg.query) > 0 {
		q := u.Query()
		for k, vs := range cfg.query {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
	}

	var reqBody io.Reader
	contentType := ""
	switch {
	case cfg.rawBody != nil:
		reqBody = cfg.rawBody
		contentType = cfg.rawCT
	case hasJSONBody:
		b, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("kakashi: marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, string(method), u.String(), reqBody)
	if err != nil {
		return zero, fmt.Errorf("kakashi: new request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range c.headers {
		req.Header[k] = append([]string(nil), vs...)
	}
	for k, vs := range cfg.headers {
		req.Header[k] = append([]string(nil), vs...)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return zero, fmt.Errorf("kakashi: request: %w", err)
	}
	defer func() {
		// Drain to allow connection reuse, then close.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return zero, &APIError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			URL:        u.String(),
			Body:       b,
		}
	}

	var result Resp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		if errors.Is(err, io.EOF) {
			return zero, nil
		}
		return zero, fmt.Errorf("kakashi: decode response: %w", err)
	}
	return result, nil
}

