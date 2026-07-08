// Package dimail is a pure-Go (CGO-free) client for the Dimail API, the mail
// hosting management API of the French government's "La Suite numérique"
// platform (https://api.osprod.dimail1.numerique.gouv.fr).
//
// The typed models in models_gen.go and the request methods in client_gen.go
// are generated from the committed OpenAPI document (openapi.json) by the
// generator in internal/gen; run "go generate ./..." to refresh them. This
// file holds the hand-written runtime: the Client, authentication, the HTTP
// transport, the generic response decoders that every generated method funnels
// through, and the typed error surface.
//
// # Authentication
//
// Dimail authenticates with HTTP Basic credentials to mint a bearer token,
// then expects that token on every other call. The typical flow is:
//
//	c := dimail.NewClient(dimail.WithBasicAuth("user", "pass"))
//	if _, err := c.Login(ctx); err != nil { // fetches and stores a token
//		return err
//	}
//	dom, err := c.GetDomain(ctx, "example.com")
//
// A Client sends a bearer token when one is set (via WithToken, SetToken or a
// prior Login) and otherwise falls back to the Basic credentials. Login relies
// on that precedence: it runs before a token exists, so the token request is
// authenticated with Basic.
package dimail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

// Version is the client library version, reported in the default User-Agent.
const Version = "0.1.0"

// DefaultBaseURL is the production Dimail API endpoint.
const DefaultBaseURL = "https://api.osprod.dimail1.numerique.gouv.fr"

// Client is a Dimail API client. It is safe for concurrent use once
// configured; SetToken mutates the stored token and should be sequenced by the
// caller if shared across goroutines during login.
type Client struct {
	// BaseURL is the API root, without a trailing slash.
	BaseURL string
	// HTTPClient performs requests. When nil, http.DefaultClient is used.
	HTTPClient *http.Client
	// UserAgent is sent with every request when non-empty.
	UserAgent string

	username string
	password string
	token    string
}

// Option configures a Client in NewClient.
type Option func(*Client)

// WithBaseURL overrides the API root (any trailing slash is trimmed).
func WithBaseURL(u string) Option {
	return func(c *Client) { c.BaseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the underlying *http.Client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.HTTPClient = h }
}

// WithBasicAuth sets the HTTP Basic credentials used to obtain a token.
func WithBasicAuth(user, pass string) Option {
	return func(c *Client) { c.username, c.password = user, pass }
}

// WithToken sets a bearer token to authenticate every request.
func WithToken(token string) Option {
	return func(c *Client) { c.token = token }
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.UserAgent = ua }
}

// NewClient builds a Client pointed at the production API by default.
func NewClient(opts ...Option) *Client {
	c := &Client{BaseURL: DefaultBaseURL, UserAgent: "go-dimail/" + Version}
	for _, o := range opts {
		o(c)
	}
	return c
}

// SetToken stores a bearer token, which takes precedence over Basic auth.
func (c *Client) SetToken(token string) { c.token = token }

// CurrentToken returns the bearer token currently in use, if any.
func (c *Client) CurrentToken() string { return c.token }

// Login obtains a bearer token using the configured Basic credentials and
// stores it on the Client for subsequent calls.
func (c *Client) Login(ctx context.Context) (*Token, error) {
	tok, err := c.GetToken(ctx, nil)
	if err != nil {
		return nil, err
	}
	if tok != nil {
		c.token = tok.AccessToken
	}
	return tok, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) applyAuth(req *http.Request) {
	switch {
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	case c.username != "":
		req.SetBasicAuth(c.username, c.password)
	}
}

// do performs a single request. body, when non-nil and not a nil pointer, is
// JSON-encoded as the request payload. out, when non-nil, receives the decoded
// JSON response body. A non-2xx status yields an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.BaseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var rdr io.Reader
	if body != nil && !isNilPtr(body) {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	c.applyAuth(req)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newAPIError(resp, data)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return err
		}
	}
	return nil
}

// isNilPtr reports whether v is a nil pointer (or an untyped nil).
func isNilPtr(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Ptr && rv.IsNil()
}

// esc escapes a value for interpolation into a URL path segment.
func esc(s string) string { return url.PathEscape(s) }

// The generated methods dispatch through these four generic decoders so that
// each method is a single delegating statement and all transport branches live
// here, tested once.

func doJSON[T any](c *Client, ctx context.Context, method, path string, query url.Values, body any) (*T, error) {
	var out T
	if err := c.do(ctx, method, path, query, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func doList[T any](c *Client, ctx context.Context, method, path string, query url.Values, body any) ([]T, error) {
	var out []T
	if err := c.do(ctx, method, path, query, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func doValue[T any](c *Client, ctx context.Context, method, path string, query url.Values, body any) (T, error) {
	var out T
	if err := c.do(ctx, method, path, query, body, &out); err != nil {
		return out, err
	}
	return out, nil
}

func doVoid(c *Client, ctx context.Context, method, path string, query url.Values, body any) error {
	return c.do(ctx, method, path, query, body, nil)
}

// APIError is returned for any non-2xx response. It carries the status, the
// request that produced it, and the raw body; Detail holds the FastAPI
// "detail" field when the body is the usual error envelope.
type APIError struct {
	StatusCode int
	Status     string
	Method     string
	URL        string
	Detail     json.RawMessage
	Body       []byte
}

// Error implements error.
func (e *APIError) Error() string {
	if len(e.Detail) > 0 {
		return fmt.Sprintf("dimail: %s %s: %d %s: %s",
			e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode), e.Detail)
	}
	return fmt.Sprintf("dimail: %s %s: %d %s",
		e.Method, e.URL, e.StatusCode, http.StatusText(e.StatusCode))
}

// NotFound reports whether the status was 404.
func (e *APIError) NotFound() bool { return e.StatusCode == http.StatusNotFound }

// Unauthorized reports whether the status was 401.
func (e *APIError) Unauthorized() bool { return e.StatusCode == http.StatusUnauthorized }

// Forbidden reports whether the status was 403.
func (e *APIError) Forbidden() bool { return e.StatusCode == http.StatusForbidden }

// Conflict reports whether the status was 409.
func (e *APIError) Conflict() bool { return e.StatusCode == http.StatusConflict }

func newAPIError(resp *http.Response, data []byte) *APIError {
	e := &APIError{StatusCode: resp.StatusCode, Status: resp.Status, Body: data}
	if resp.Request != nil {
		e.Method = resp.Request.Method
		e.URL = resp.Request.URL.String()
	}
	var env struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(data, &env) == nil {
		e.Detail = env.Detail
	}
	return e
}
