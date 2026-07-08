package dimail

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientDefaults(t *testing.T) {
	c := NewClient()
	if c.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", c.BaseURL, DefaultBaseURL)
	}
	if c.UserAgent != "go-dimail/"+Version {
		t.Fatalf("UserAgent = %q", c.UserAgent)
	}
	if c.httpClient() != http.DefaultClient {
		t.Fatal("httpClient() should default to http.DefaultClient")
	}
}

func TestOptions(t *testing.T) {
	hc := &http.Client{}
	c := NewClient(
		WithBaseURL("https://example.test/"),
		WithHTTPClient(hc),
		WithBasicAuth("alice", "secret"),
		WithToken("tok"),
		WithUserAgent("ua/1"),
	)
	if c.BaseURL != "https://example.test" {
		t.Fatalf("trailing slash not trimmed: %q", c.BaseURL)
	}
	if c.httpClient() != hc {
		t.Fatal("WithHTTPClient not applied")
	}
	if c.username != "alice" || c.password != "secret" {
		t.Fatal("WithBasicAuth not applied")
	}
	if c.CurrentToken() != "tok" || c.UserAgent != "ua/1" {
		t.Fatal("WithToken/WithUserAgent not applied")
	}
	c.SetToken("tok2")
	if c.CurrentToken() != "tok2" {
		t.Fatal("SetToken not applied")
	}
}

// captureServer records the last request it saw and replies with a fixed body.
type captureServer struct {
	status  int
	body    string
	gotAuth string
	gotUA   string
	gotCT   string
	gotPath string
}

func (s *captureServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.gotAuth = r.Header.Get("Authorization")
		s.gotUA = r.Header.Get("User-Agent")
		s.gotCT = r.Header.Get("Content-Type")
		s.gotPath = r.URL.RequestURI()
		if s.status != 0 {
			w.WriteHeader(s.status)
		}
		_, _ = io.WriteString(w, s.body)
	}
}

func TestDoDecodesJSONAndSendsBearer(t *testing.T) {
	srv := &captureServer{body: `{"tech_name":"t","host_name":"h"}`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	c := NewClient(WithBaseURL(ts.URL), WithToken("abc"), WithUserAgent("ua/x"))
	out, err := c.GetTechDomain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.TechName != "t" || out.HostName != "h" {
		t.Fatalf("decoded = %+v", out)
	}
	if srv.gotAuth != "Bearer abc" {
		t.Fatalf("auth = %q", srv.gotAuth)
	}
	if srv.gotUA != "ua/x" {
		t.Fatalf("ua = %q", srv.gotUA)
	}
}

func TestDoSendsBodyAndBasicAuthAndNoUA(t *testing.T) {
	srv := &captureServer{body: `{"nexthop":"x@y"}`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	// No token -> Basic auth; empty UserAgent -> header omitted.
	c := NewClient(WithBaseURL(ts.URL), WithBasicAuth("u", "p"), WithUserAgent(""))
	_, err := c.PostForward(context.Background(), "example.com", "bob", &CreateForward{Nexthop: "x@y"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(srv.gotAuth, "Basic ") {
		t.Fatalf("auth = %q, want Basic", srv.gotAuth)
	}
	if srv.gotCT != "application/json" {
		t.Fatalf("content-type = %q", srv.gotCT)
	}
	// With an empty UserAgent we set no header; Go's transport supplies its own
	// default, so we only assert our custom UA was not sent.
	if strings.HasPrefix(srv.gotUA, "go-dimail/") {
		t.Fatalf("ua = %q, want no custom UA", srv.gotUA)
	}
	if srv.gotPath != "/domains/example.com/forwards/bob" {
		t.Fatalf("path = %q", srv.gotPath)
	}
}

func TestDoNoAuthWhenUnconfigured(t *testing.T) {
	srv := &captureServer{body: `{}`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithUserAgent(""))
	if _, err := c.GetVersion(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.gotAuth != "" {
		t.Fatalf("auth = %q, want empty", srv.gotAuth)
	}
}

func TestLogin(t *testing.T) {
	srv := &captureServer{body: `{"access_token":"TKN","token_type":"bearer"}`}
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithBasicAuth("u", "p"))
	tok, err := c.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "TKN" {
		t.Fatalf("token = %q", tok.AccessToken)
	}
	// The token endpoint must be reached with Basic auth (no token yet).
	if !strings.HasPrefix(srv.gotAuth, "Basic ") {
		t.Fatalf("login auth = %q", srv.gotAuth)
	}
	// Subsequent calls use the stored bearer token.
	if c.CurrentToken() != "TKN" {
		t.Fatalf("stored token = %q", c.CurrentToken())
	}
}

func TestLoginError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"bad creds"}`)
	}))
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithBasicAuth("u", "p"))
	if _, err := c.Login(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if c.CurrentToken() != "" {
		t.Fatal("token should remain unset on failure")
	}
}

func TestAPIErrorFromResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"nope"}`)
	}))
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithToken("t"))
	_, err := c.GetDomain(context.Background(), "absent.example")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T (%v)", err, err)
	}
	if !apiErr.NotFound() || apiErr.Unauthorized() || apiErr.Forbidden() || apiErr.Conflict() {
		t.Fatalf("classification wrong for %d", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Error(), "nope") || !strings.Contains(apiErr.Error(), "404") {
		t.Fatalf("Error() = %q", apiErr.Error())
	}
}

func TestAPIErrorClassifiers(t *testing.T) {
	for _, tc := range []struct {
		code                                 int
		nf, unauth, forbidden, conflict bool
	}{
		{http.StatusUnauthorized, false, true, false, false},
		{http.StatusForbidden, false, false, true, false},
		{http.StatusConflict, false, false, false, true},
	} {
		e := &APIError{StatusCode: tc.code}
		if e.NotFound() != tc.nf || e.Unauthorized() != tc.unauth ||
			e.Forbidden() != tc.forbidden || e.Conflict() != tc.conflict {
			t.Fatalf("classifiers wrong for %d", tc.code)
		}
	}
}

func TestNewAPIErrorNoDetailNoRequest(t *testing.T) {
	// Body that is not the FastAPI envelope -> Detail stays nil; nil Request
	// exercises the guarded branch and leaves Method/URL empty.
	resp := &http.Response{StatusCode: 500, Status: "500 Internal Server Error"}
	e := newAPIError(resp, []byte("not json"))
	if e.Detail != nil {
		t.Fatalf("Detail = %s, want nil", e.Detail)
	}
	if e.Method != "" || e.URL != "" {
		t.Fatal("Method/URL should be empty with nil Request")
	}
	if !strings.Contains(e.Error(), "500") || strings.Contains(e.Error(), ": :") {
		t.Fatalf("Error() = %q", e.Error())
	}
}

func TestDoUnmarshalError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{not valid json`)
	}))
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithToken("t"))
	if _, err := c.GetVersion(context.Background()); err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestDoBodyMarshalError(t *testing.T) {
	c := NewClient(WithBaseURL("http://127.0.0.1:0"))
	// chan cannot be JSON-encoded; do must surface the marshal error before
	// any network activity.
	err := c.do(context.Background(), "POST", "/x", nil, make(chan int), nil)
	if err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestDoRequestBuildError(t *testing.T) {
	c := NewClient(WithBaseURL("http://127.0.0.1:0"))
	// An invalid HTTP method makes http.NewRequestWithContext fail.
	err := c.do(context.Background(), "BAD METHOD", "/x", nil, nil, nil)
	if err == nil {
		t.Fatal("expected request build error")
	}
}

func TestDoTransportError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := ts.URL
	ts.Close() // now refused
	c := NewClient(WithBaseURL(url), WithToken("t"))
	if _, err := c.GetVersion(context.Background()); err == nil {
		t.Fatal("expected transport error")
	}
}

type errRoundTripper struct{}

type errBody struct{}

func (errBody) Read([]byte) (int, error) { return 0, errors.New("boom") }
func (errBody) Close() error             { return nil }

func (errRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: 200,
		Body:       errBody{},
		Request:    r,
		Header:     make(http.Header),
	}, nil
}

func TestDoBodyReadError(t *testing.T) {
	c := NewClient(WithBaseURL("http://example.test"),
		WithHTTPClient(&http.Client{Transport: errRoundTripper{}}), WithToken("t"))
	if _, err := c.GetVersion(context.Background()); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want read error, got %v", err)
	}
}

func TestIsNilPtr(t *testing.T) {
	if !isNilPtr(nil) {
		t.Fatal("nil should be nil ptr")
	}
	var p *int
	if !isNilPtr(p) {
		t.Fatal("typed nil pointer should be nil ptr")
	}
	x := 3
	if isNilPtr(&x) {
		t.Fatal("non-nil pointer should not be nil ptr")
	}
	if isNilPtr(5) {
		t.Fatal("non-pointer should not be nil ptr")
	}
}

func TestEsc(t *testing.T) {
	if got := esc("a b/c"); got != "a%20b%2Fc" {
		t.Fatalf("esc = %q", got)
	}
}

// TestGenericDecodersError checks that each generic decoder propagates a
// transport-layer error (the success paths are covered by the smoke test).
func TestGenericDecodersError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"detail":"x"}`)
	}))
	defer ts.Close()
	c := NewClient(WithBaseURL(ts.URL), WithToken("t"))
	ctx := context.Background()

	if _, err := doJSON[Token](c, ctx, "GET", "/x", nil, nil); err == nil {
		t.Fatal("doJSON should error")
	}
	if _, err := doList[Domain](c, ctx, "GET", "/x", nil, nil); err == nil {
		t.Fatal("doList should error")
	}
	if _, err := doValue[bool](c, ctx, "GET", "/x", nil, nil); err == nil {
		t.Fatal("doValue should error")
	}
	if err := doVoid(c, ctx, "GET", "/x", nil, nil); err == nil {
		t.Fatal("doVoid should error")
	}
}

// Ensure json is referenced (Detail is json.RawMessage) so the import is used
// even if the rest of the file changes.
var _ = json.RawMessage(nil)
