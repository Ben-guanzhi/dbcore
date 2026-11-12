package main

// A thin HTTP seam shared by the REST-based providers (Nacos, Milvus, Pulsar). None of
// them needs an SDK — each exposes a plain HTTP API — so the providers stay dependency
// free and, more importantly, testable: a test injects an *http.Client pointed at a
// httptest.Server and the provider cannot tell the difference from the real service.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// httpDoer is the piece a test swaps. *http.Client satisfies it.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// httpConsole is the shared state of a REST-based session: where to talk to, how, and
// an optional bearer token (Nacos and Milvus both issue one on login).
type httpConsole struct {
	doer  httpDoer
	base  string
	token string
}

// newHTTPConsole builds the base URL from a connection's host/port. The scheme and an
// explicit base are overridable through Options so a test (or a reverse-proxied
// deployment) can point the console anywhere without a rebuild.
func newHTTPConsole(p hostPort) *httpConsole {
	scheme := p.Scheme
	if scheme == "" {
		scheme = "http"
	}
	base := p.Base
	if base == "" {
		base = fmt.Sprintf("%s://%s", scheme, p.HostPort())
	}
	return &httpConsole{doer: &http.Client{Timeout: 30 * time.Second}, base: strings.TrimRight(base, "/")}
}

// get issues a GET with query parameters and returns the body.
func (h *httpConsole) get(ctx context.Context, path string, query url.Values) ([]byte, int, error) {
	u := h.base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return h.do(ctx, http.MethodGet, u, nil, "")
}

// postJSON issues a POST with a JSON body and returns the body.
func (h *httpConsole) postJSON(ctx context.Context, path string, body any) ([]byte, int, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	return h.do(ctx, http.MethodPost, h.base+path, bytes.NewReader(buf), "application/json")
}

// postForm issues a POST with form-encoded fields, which is what Nacos's publish API
// wants (it predates its JSON endpoints).
func (h *httpConsole) postForm(ctx context.Context, path string, fields url.Values) ([]byte, int, error) {
	return h.do(ctx, http.MethodPost, h.base+path,
		strings.NewReader(fields.Encode()), "application/x-www-form-urlencoded")
}

func (h *httpConsole) do(ctx context.Context, method, u string, body io.Reader, contentType string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.doer.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// decodeJSON unmarshals a response body, treating a non-2xx as an error that carries
// the body — an HTTP console that swallowed the status would return "no rows" for what
// is really a 404 or a 500, and the user would have no way to tell.
func decodeJSON(body []byte, status int, out any) error {
	if status >= 400 {
		return fmt.Errorf("HTTP %d: %s", status, truncate(string(body), 300))
	}
	if len(body) == 0 {
		return fmt.Errorf("空响应")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("响应不是 JSON: %w（%s）", err, truncate(string(body), 200))
	}
	return nil
}

// truncate keeps an error message readable: a whole config body or a stack of JSON in
// an error toast is noise.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// hostPort is the connection coordinates a provider needs, extracted from the
// connection's params so the providers do not each re-parse them.
type hostPort struct {
	Host   string
	Port   string
	Scheme string
	Base   string
	User   string
	Pass   string
	Token  string
}

func hostPortOf(p hostPort) string { return p.HostPort() }

// HostPort joins host and port, defaulting the port so "localhost" still builds a URL.
func (h hostPort) HostPort() string {
	if h.Port == "" {
		return h.Host
	}
	return h.Host + ":" + h.Port
}
