package sophos

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultAPIPath is the prefix every Firewall Configuration API v1 route
// hangs off. The OpenAPI server block spells the base URL as
// https://{firewallHost}/api/firewall-config/v1, where firewallHost is the
// address *and* the admin port, e.g. 172.16.16.16:4444.
const DefaultAPIPath = "/api/firewall-config/v1"

// maxPageSize is the ceiling the API advertises in pages.maxSize. Asking
// for more is rejected, so the client clamps to it.
const maxPageSize = 200

// rulesPath is the collection every IPv4 rule route hangs off, and
// movePath is the one sub-resource under it.
var (
	rulesPath = []string{"firewall", "rules", "ipv4"}
	movePath  = []string{"firewall", "rules", "ipv4", "move"}
)

// rulePath builds the segments for a single rule, rejecting identifiers
// that would not stay in the path: a "/" would silently address a
// different endpoint, and "." or ".." would walk out of the collection.
func rulePath(idOrName string) ([]string, error) {
	name := strings.TrimSpace(idOrName)
	switch {
	case name == "":
		return nil, errors.New("sophos: rule id or name is required")
	case strings.Contains(name, "/"):
		return nil, fmt.Errorf("sophos: rule id or name %q contains a path separator", idOrName)
	case name == "." || name == "..":
		return nil, fmt.Errorf("sophos: invalid rule id or name %q", idOrName)
	}
	return append(append([]string{}, rulesPath...), name), nil
}

// Config describes how to reach one firewall.
type Config struct {
	// Host is "address:port" (e.g. "172.16.16.16:4444") or a full base URL
	// if the firewall sits behind a proxy with a different path.
	Host string

	// APIKey is sent as "Authorization: Bearer <key>". Generate it in the
	// firewall under the administrator profile that owns the API access;
	// the key inherits that account's permissions.
	APIKey string

	// Insecure skips TLS verification. Sophos appliances ship with a
	// self-signed WebAdmin certificate, so this is on by necessity in most
	// labs — leave it off wherever the appliance has a real certificate.
	Insecure bool

	// Timeout bounds a single HTTP request. Zero means 30s.
	Timeout time.Duration

	// PageSize is the page size used when listing. Zero means 200.
	PageSize int
}

// Client talks to one firewall's configuration API.
type Client struct {
	base  *url.URL
	key   string
	page  int
	httpc *http.Client
}

// APIError is a non-2xx response, decoded into the error envelope the API
// documents ({"error","message","code"}).
type APIError struct {
	Status  int    `json:"-"`
	Method  string `json:"-"`
	Path    string `json:"-"`
	Err     string `json:"error"`
	Message string `json:"message"`
	Code    string `json:"code"`
	Body    string `json:"-"`
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: HTTP %d", e.Method, e.Path, e.Status)
	if e.Err != "" {
		fmt.Fprintf(&b, " (%s)", e.Err)
	}
	switch {
	case e.Message != "":
		fmt.Fprintf(&b, ": %s", e.Message)
	case e.Body != "":
		fmt.Fprintf(&b, ": %s", truncate(e.Body, 300))
	}
	return b.String()
}

// NotFound reports whether the request failed because the rule is gone.
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }

// NewClient validates cfg and builds a client.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("sophos: host is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("sophos: API key is required")
	}

	base, err := baseURL(cfg.Host)
	if err != nil {
		return nil, err
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	page := cfg.PageSize
	if page <= 0 || page > maxPageSize {
		page = maxPageSize
	}

	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: cfg.Insecure},
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &Client{
		base:  base,
		key:   cfg.APIKey,
		page:  page,
		httpc: &http.Client{Timeout: timeout, Transport: tr},
	}, nil
}

// baseURL turns a bare "host:port" into the full API base URL, and leaves
// an explicit URL alone beyond appending the API path when it is missing.
func baseURL(host string) (*url.URL, error) {
	raw := strings.TrimSpace(host)
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("sophos: invalid host %q: %w", host, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("sophos: invalid host %q: no host component", host)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if u.Path == "" {
		u.Path = DefaultAPIPath
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u, nil
}

// BaseURL returns the resolved API base, for logs and dry-run output.
func (c *Client) BaseURL() string { return c.base.String() }

// do performs one request and decodes the response into out (which may be
// nil to discard the body).
//
// segments are unescaped path elements appended to the base URL. They go
// through JoinPath rather than string concatenation so that a rule name
// carrying a space or a "#" is escaped exactly once — concatenating an
// already-escaped name into URL.Path escapes it a second time and asks the
// firewall for a resource that does not exist.
func (c *Client) do(ctx context.Context, method string, segments []string, query url.Values, body, out any) error {
	u := c.base.JoinPath(segments...)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	path := "/" + strings.Join(segments, "/")

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("sophos: encoding request body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return fmt.Errorf("sophos: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("sophos: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	// Cap the read: a misrouted request can land on the WebAdmin UI and
	// return a megabyte of HTML.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return fmt.Errorf("sophos: %s %s: reading response: %w", method, path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(raw)}
		_ = json.Unmarshal(raw, apiErr) // best effort; body may not be JSON
		return apiErr
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("sophos: %s %s: decoding response: %w", method, path, err)
	}
	return nil
}

// ListIPv4Rules returns every IPv4 rule, in evaluation order.
//
// Order is the whole point for this tool: the API returns rules in the
// order the firewall evaluates them, and nothing in the rule object itself
// records a position, so the slice index *is* the rule's position.
func (c *Client) ListIPv4Rules(ctx context.Context) ([]Rule, error) {
	var all []Rule
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(c.page))
		q.Set("pageTotal", "true")

		var list RuleList
		if err := c.do(ctx, http.MethodGet, rulesPath, q, nil, &list); err != nil {
			return nil, err
		}
		all = append(all, list.Items...)

		// Stop on the reported total when we have one, and otherwise on the
		// first short page. A page that comes back empty always stops us,
		// so a firewall that ignores pageTotal cannot spin this loop.
		if len(list.Items) == 0 {
			break
		}
		if list.Pages.Total > 0 && page >= list.Pages.Total {
			break
		}
		if list.Pages.Total == 0 && len(list.Items) < c.page {
			break
		}
	}
	return all, nil
}

// GetIPv4Rule fetches a single rule by UUID or name.
func (c *Client) GetIPv4Rule(ctx context.Context, idOrName string) (*Rule, error) {
	segments, err := rulePath(idOrName)
	if err != nil {
		return nil, err
	}
	var rule Rule
	if err := c.do(ctx, http.MethodGet, segments, nil, nil, &rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

// CreateIPv4Rule creates a rule. The rule's Position (and ReferenceItem
// for before/after) decides where it lands.
func (c *Client) CreateIPv4Rule(ctx context.Context, rule *Rule) (*Rule, error) {
	if rule == nil {
		return nil, errors.New("sophos: nil rule")
	}
	var created Rule
	if err := c.do(ctx, http.MethodPost, rulesPath, nil, rule, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// UpdateIPv4Rule PATCHes a rule. patch carries only the fields to change —
// pass a map or a Rule with everything else left zero.
func (c *Client) UpdateIPv4Rule(ctx context.Context, idOrName string, patch any) (*Rule, error) {
	segments, err := rulePath(idOrName)
	if err != nil {
		return nil, err
	}
	var updated Rule
	if err := c.do(ctx, http.MethodPatch, segments, nil, patch, &updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// DeleteIPv4Rule removes a rule by UUID or name.
func (c *Client) DeleteIPv4Rule(ctx context.Context, idOrName string) error {
	segments, err := rulePath(idOrName)
	if err != nil {
		return err
	}
	var res struct {
		Deleted bool `json:"deleted"`
	}
	if err := c.do(ctx, http.MethodDelete, segments, nil, nil, &res); err != nil {
		return err
	}
	if !res.Deleted {
		return fmt.Errorf("sophos: firewall reported rule %q as not deleted", idOrName)
	}
	return nil
}

// MoveRequest is the body of POST /firewall/rules/ipv4/move.
type MoveRequest struct {
	Name          string    `json:"name,omitempty"`
	Position      string    `json:"position"`
	ReferenceItem *NamedRef `json:"referenceItem,omitempty"`
}

// Rule positions accepted by the move and create endpoints.
const (
	PositionTop    = "top"
	PositionBottom = "bottom"
	PositionBefore = "before"
	PositionAfter  = "after"
)

// MoveIPv4Rule repositions a rule. Unlike update and delete, the move
// endpoint identifies both the rule and the anchor by name only — a UUID
// is not accepted there.
func (c *Client) MoveIPv4Rule(ctx context.Context, name, position, anchor string) error {
	switch position {
	case PositionTop, PositionBottom:
		if anchor != "" {
			return fmt.Errorf("sophos: position %q takes no anchor rule", position)
		}
	case PositionBefore, PositionAfter:
		if anchor == "" {
			return fmt.Errorf("sophos: position %q requires an anchor rule", position)
		}
	default:
		return fmt.Errorf("sophos: invalid position %q", position)
	}
	if name == "" {
		return errors.New("sophos: move requires the rule name")
	}

	req := MoveRequest{Name: name, Position: position}
	if anchor != "" {
		req.ReferenceItem = &NamedRef{Name: anchor}
	}
	return c.do(ctx, http.MethodPost, movePath, nil, req, nil)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
