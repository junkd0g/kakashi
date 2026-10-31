# kakashi

[![Go Report Card](https://goreportcard.com/badge/github.com/junkd0g/kakashi)](https://goreportcard.com/report/github.com/junkd0g/kakashi)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](https://opensource.org/licenses/MIT)
[![GoDoc](https://pkg.go.dev/badge/github.com/junkd0g/kakashi.svg)](https://pkg.go.dev/github.com/junkd0g/kakashi)

`kakashi` is a small, generic, strongly-typed HTTP client for Go. Endpoints
are declared once with their request and response types and executed against
a `*Client` — no casts, no `interface{}`, no boilerplate `json.Unmarshal`.

## Why

Most Go services that call an HTTP API end up with the same five lines glued
together over and over: build the URL, marshal a body, set headers, decode
the response, branch on status code. `kakashi` keeps the call site one line
and pushes the rest into the type system: the response type lives on the
endpoint and the compiler does the rest. Non-2xx responses come back as a
typed `*APIError`, so callers can `errors.As` instead of string-matching.

## Features

- Strongly-typed request and response bodies via Go generics.
- `context.Context` on every call.
- Typed `*APIError` carrying status code, status text, URL, and (bounded)
  body — switch on it with `errors.As`. Error URLs omit user information,
  query parameters, and fragments.
- Functional options on `New` (`WithHTTPClient`, `WithTimeout`,
  `WithDefaultHeader`).
- Per-request options (`WithHeader`, `WithQuery`, `WithRawBody`,
  `WithAllowEmptyResponse`).
- URL composition via `(*url.URL).JoinPath` — no `//` or missing-slash bugs.
- Successful responses require exactly one JSON value, with optional trailing
  whitespace. Error body reads are capped with `io.LimitReader`.
- Closes responses without draining unread data. Fully consumed responses can
  reuse their connections; oversized errors sacrifice reuse to avoid waiting
  for an unbounded response tail.
- Closed-set HTTP method validation at endpoint construction — typos panic
  at package init, not at request time.
- Zero runtime dependencies.

## Requirements

- Go 1.25 or newer (uses generics and `(*url.URL).JoinPath`).

## Installation

```sh
go get -u github.com/junkd0g/kakashi
```

## Quick start

```go
package main

import (
    "context"
    "fmt"

    "github.com/junkd0g/kakashi"
)

type Post struct {
    ID    int    `json:"id"`
    Title string `json:"title"`
}

var GetPost1 = kakashi.NewEndpoint[Post](kakashi.MethodGet, "/posts/1")

func main() {
    client := kakashi.New("https://jsonplaceholder.typicode.com")

    post, err := kakashi.Call(context.Background(), client, GetPost1)
    if err != nil {
        panic(err)
    }
    fmt.Printf("Title: %s\n", post.Title)
}
```

## Usage

### Body-less endpoints

```go
var GetUser = kakashi.NewEndpoint[User](kakashi.MethodGet, "/users/1")

user, err := kakashi.Call(ctx, client, GetUser)
```

### Endpoints with a typed JSON body

```go
var CreateUser = kakashi.NewBodyEndpoint[User, User](kakashi.MethodPost, "/users")

created, err := kakashi.CallBody(ctx, client, CreateUser, User{Name: "Alice"})
```

The request body type is checked at compile time — passing the wrong struct
is a compile error, not a runtime surprise.

### Client options

```go
client := kakashi.New(
    "https://api.example.com",
    kakashi.WithTimeout(30*time.Second),
    kakashi.WithDefaultHeader("Authorization", "Bearer "+token),
)
```

`WithHTTPClient` copies the supplied client's configuration while sharing its
transport and cookie jar. `WithTimeout` therefore does not change the supplied
client or other wrappers. Options apply in order: place `WithTimeout` after
`WithHTTPClient` to override the supplied timeout.

### Per-request options

```go
users, err := kakashi.Call(ctx, client, GetUsers,
    kakashi.WithQuery(url.Values{"page": {"2"}, "limit": {"50"}}),
    kakashi.WithHeader("X-Request-ID", reqID),
)
```

Repeated `WithQuery` options append values to one another and to the base URL's
query parameters. Endpoint paths must contain valid percent escapes; for example,
a literal percent sign must be written as `%25`.

### Empty responses

HEAD requests and responses with status 204 or 205 return the response type's zero
value without decoding a body. Other successful responses must contain exactly
one JSON value. To allow an endpoint that legitimately returns an empty or
whitespace-only body with another successful status, pass
`kakashi.WithAllowEmptyResponse()`. This option still rejects malformed JSON and
multiple JSON values.

### Typed error handling

Any non-2xx response is returned as `*APIError`:

```go
_, err := kakashi.Call(ctx, client, GetUser)
var apiErr *kakashi.APIError
if errors.As(err, &apiErr) {
    switch apiErr.StatusCode {
    case http.StatusNotFound:
        // ...
    case http.StatusUnauthorized:
        // ...
    }
}
```

The response body on `APIError.Body` is read with a 64 KiB cap so a verbose
HTML 500 page can't blow up logs. The remaining body is closed without draining.
`APIError.URL` and its error string omit URL user information, query parameters,
and fragments; outgoing request authentication and query parameters are unchanged.

### Sending a raw (non-JSON) body

```go
form := url.Values{"grant_type": {"client_credentials"}}.Encode()
_, err := kakashi.Call(ctx, client, TokenEndpoint,
    kakashi.WithRawBody(strings.NewReader(form), "application/x-www-form-urlencoded"),
)
```

## API Reference

### Types

```go
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

type Endpoint[Resp any] struct {
    Method Method
    Path   string
}

type BodyEndpoint[Req, Resp any] struct {
    Method Method
    Path   string
}

type Client struct {
    BaseURL    string
    HTTPClient *http.Client
    // unexported header map
}

type APIError struct {
    StatusCode int
    Status     string
    URL        string
    Body       []byte
}
```

### Constructors

```go
func New(baseURL string, opts ...Option) *Client
func NewEndpoint[Resp any](method Method, path string) Endpoint[Resp]
func NewBodyEndpoint[Req, Resp any](method Method, path string) BodyEndpoint[Req, Resp]
```

### Execution

```go
func Call[Resp any](
    ctx context.Context, c *Client, e Endpoint[Resp], opts ...RequestOption,
) (Resp, error)

func CallBody[Req, Resp any](
    ctx context.Context, c *Client, e BodyEndpoint[Req, Resp], body Req, opts ...RequestOption,
) (Resp, error)
```

### Client options

```go
func WithHTTPClient(h *http.Client) Option
func WithTimeout(d time.Duration) Option
func WithDefaultHeader(key, value string) Option
```

### Request options

```go
func WithHeader(key, value string) RequestOption
func WithQuery(values url.Values) RequestOption
func WithRawBody(r io.Reader, contentType string) RequestOption
func WithAllowEmptyResponse() RequestOption
```

## Testing

```sh
go test ./...
```

With coverage:

```sh
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

## Contributing

1. Fork the repository.
2. Create a feature branch (`git checkout -b feature/your-feature`).
3. Commit your changes.
4. Push to the branch (`git push origin feature/your-feature`).
5. Open a pull request.

## License

This project is licensed under the MIT License — see the [LICENSE](LICENSE)
file for details.

## Author

Iordanis Paschalidis — [@junkd0g](https://github.com/junkd0g)
