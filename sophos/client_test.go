package sophos

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// testClient points a client at an httptest server. The test servers speak
// plain HTTP, so the host carries the scheme and the client keeps it.
func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	c, err := NewClient(Config{Host: baseURL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientValidatesConfig(t *testing.T) {
	if _, err := NewClient(Config{APIKey: "k"}); err == nil {
		t.Error("a client without a host should not be built")
	}
	if _, err := NewClient(Config{Host: "10.0.0.1:4444"}); err == nil {
		t.Error("a client without an API key should not be built")
	}
}

func TestBaseURLResolution(t *testing.T) {
	cases := map[string]string{
		"10.0.0.1:4444":                         "https://10.0.0.1:4444" + DefaultAPIPath,
		"https://fw.example.com:4444":           "https://fw.example.com:4444" + DefaultAPIPath,
		"https://fw.example.com:4444/":          "https://fw.example.com:4444" + DefaultAPIPath,
		"https://proxy.example.com/sophos/v1":   "https://proxy.example.com/sophos/v1",
		"http://127.0.0.1:8080":                 "http://127.0.0.1:8080" + DefaultAPIPath,
		"https://fw.example.com:4444?ignored=1": "https://fw.example.com:4444" + DefaultAPIPath,
	}
	for in, want := range cases {
		c, err := NewClient(Config{Host: in, APIKey: "k"})
		if err != nil {
			t.Errorf("NewClient(%q): %v", in, err)
			continue
		}
		if got := c.BaseURL(); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := NewClient(Config{Host: "://nope", APIKey: "k"}); err == nil {
		t.Error("an unparseable host should be rejected")
	}
}

func TestListSendsBearerTokenAndPaginates(t *testing.T) {
	var seenAuth string
	var seenSizes []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, DefaultAPIPath+"/firewall/rules/ipv4"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		seenAuth = r.Header.Get("Authorization")
		seenSizes = append(seenSizes, r.URL.Query().Get("pageSize"))
		if r.URL.Query().Get("pageTotal") != "true" {
			t.Errorf("pageTotal not requested: %q", r.URL.RawQuery)
		}

		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		resp := RuleList{Pages: Pages{Current: page, Total: 3, Size: 200, MaxSize: 200}}
		resp.Items = []Rule{rule(fmt.Sprintf("rule-%d", page), ActionAccept)}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).ListIPv4Rules(context.Background())
	if err != nil {
		t.Fatalf("ListIPv4Rules: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rules, want one per page across 3 pages", len(got))
	}
	// Order is the firewall's evaluation order, so pages must be appended
	// in sequence rather than merged in any other way.
	for i, want := range []string{"rule-1", "rule-2", "rule-3"} {
		if got[i].Name != want {
			t.Errorf("rule %d = %q, want %q", i, got[i].Name, want)
		}
	}
	if seenAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want a bearer token", seenAuth)
	}
	for _, size := range seenSizes {
		if size != "200" {
			t.Errorf("pageSize = %q, want the documented maximum of 200", size)
		}
	}
}

func TestListStopsOnAShortPageWhenNoTotalIsReturned(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		// No Pages.Total: the firewall ignored pageTotal.
		_ = json.NewEncoder(w).Encode(RuleList{
			Items: []Rule{rule("only", ActionAccept)},
			Pages: Pages{Current: 1, Size: 200, MaxSize: 200},
		})
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).ListIPv4Rules(context.Background())
	if err != nil {
		t.Fatalf("ListIPv4Rules: %v", err)
	}
	if len(got) != 1 || pages != 1 {
		t.Errorf("got %d rules over %d requests, want 1 and 1", len(got), pages)
	}
}

func TestListStopsOnAnEmptyPage(t *testing.T) {
	// A firewall that reports a total it cannot fill must not spin the
	// client forever.
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		resp := RuleList{Pages: Pages{Current: pages, Total: 99, Size: 200, MaxSize: 200}}
		if pages == 1 {
			resp.Items = []Rule{rule("only", ActionAccept)}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).ListIPv4Rules(context.Background())
	if err != nil {
		t.Fatalf("ListIPv4Rules: %v", err)
	}
	if len(got) != 1 || pages != 2 {
		t.Errorf("got %d rules over %d requests, want 1 and 2", len(got), pages)
	}
}

func TestUpdateSendsAPatch(t *testing.T) {
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		_, _ = w.Write([]byte(`{"id":"abc","name":"quiet","logTraffic":true}`))
	}))
	defer srv.Close()

	got, err := testClient(t, srv.URL).UpdateIPv4Rule(context.Background(), "abc", map[string]any{"logTraffic": true})
	if err != nil {
		t.Fatalf("UpdateIPv4Rule: %v", err)
	}
	if method != http.MethodPatch {
		t.Errorf("method = %s, want PATCH", method)
	}
	if want := DefaultAPIPath + "/firewall/rules/ipv4/abc"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if !strings.Contains(body, `"logTraffic":true`) {
		t.Errorf("body = %s", body)
	}
	if !got.IsLogging() {
		t.Error("decoded rule should carry the new logTraffic value")
	}
}

func TestDeleteRequiresTheFirewallToConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"deleted":false}`))
	}))
	defer srv.Close()

	err := testClient(t, srv.URL).DeleteIPv4Rule(context.Background(), "abc")
	if err == nil || !strings.Contains(err.Error(), "not deleted") {
		t.Errorf("a deleted:false response must be an error, got %v", err)
	}
}

func TestMoveValidatesPositionAndAnchor(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := DefaultAPIPath + "/firewall/rules/ipv4/move"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ctx := context.Background()

	if err := c.MoveIPv4Rule(ctx, "a", PositionBefore, "anchor"); err != nil {
		t.Fatalf("MoveIPv4Rule: %v", err)
	}
	if !strings.Contains(body, `"position":"before"`) || !strings.Contains(body, `"name":"anchor"`) {
		t.Errorf("body = %s", body)
	}

	// The API rejects these combinations; catching them locally saves a
	// round trip and a confusing 400.
	if err := c.MoveIPv4Rule(ctx, "a", PositionBefore, ""); err == nil {
		t.Error("before without an anchor should be rejected")
	}
	if err := c.MoveIPv4Rule(ctx, "a", PositionTop, "anchor"); err == nil {
		t.Error("top with an anchor should be rejected")
	}
	if err := c.MoveIPv4Rule(ctx, "a", "sideways", ""); err == nil {
		t.Error("an unknown position should be rejected")
	}
	if err := c.MoveIPv4Rule(ctx, "", PositionTop, ""); err == nil {
		t.Error("a move without a rule name should be rejected")
	}
}

func TestAPIErrorCarriesTheFirewallsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"notFound","message":"Resource not found.","code":"404"}`))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).GetIPv4Rule(context.Background(), "missing")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error = %T (%v), want *APIError", err, err)
	}
	if !apiErr.NotFound() {
		t.Errorf("NotFound() = false for a 404")
	}
	for _, want := range []string{"404", "notFound", "Resource not found."} {
		if !strings.Contains(apiErr.Error(), want) {
			t.Errorf("error %q does not mention %q", apiErr.Error(), want)
		}
	}
}

func TestNonJSONErrorBodyIsStillReported(t *testing.T) {
	// A misrouted request lands on WebAdmin and gets HTML back. The error
	// has to say something useful rather than "unexpected character".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("<html><body>Sign in</body></html>"))
	}))
	defer srv.Close()

	_, err := testClient(t, srv.URL).GetIPv4Rule(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the status code", err)
	}
	if !strings.Contains(err.Error(), "Sign in") {
		t.Errorf("error should include the body it could not parse, got %v", err)
	}
}

func TestCreateSendsTheRule(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		_, _ = w.Write([]byte(`{"id":"new-id","name":"allow-dns"}`))
	}))
	defer srv.Close()

	in := rule("allow-dns", ActionAccept, services("dns"))
	in.Position = PositionTop
	got, err := testClient(t, srv.URL).CreateIPv4Rule(context.Background(), &in)
	if err != nil {
		t.Fatalf("CreateIPv4Rule: %v", err)
	}
	if got.ID != "new-id" {
		t.Errorf("created rule = %+v", got)
	}
	if !strings.Contains(body, `"position":"top"`) {
		t.Errorf("body should carry the position: %s", body)
	}
	if _, err := testClient(t, srv.URL).CreateIPv4Rule(context.Background(), nil); err == nil {
		t.Error("creating a nil rule should be rejected")
	}
}

func TestRuleRefPrefersTheID(t *testing.T) {
	r := Rule{ID: "uuid", Name: "named"}
	if got := r.Ref(); got != "uuid" {
		t.Errorf("Ref() = %q, want the ID", got)
	}
	r.ID = ""
	if got := r.Ref(); got != "named" {
		t.Errorf("Ref() = %q, want the name", got)
	}
}

func TestRuleDefaults(t *testing.T) {
	// enabled defaults to true and logTraffic to false, per the API schema.
	var r Rule
	if !r.IsEnabled() {
		t.Error("a rule with no enabled field should count as enabled")
	}
	if r.IsLogging() {
		t.Error("a rule with no logTraffic field should count as not logging")
	}
}

func TestRuleNamesAreEscapedExactlyOnce(t *testing.T) {
	// SFOS rule names may contain spaces. Escaping the name and then
	// concatenating it into URL.Path escapes it a second time, and the
	// firewall is asked for a rule called "allow%20lan" that does not
	// exist — so assert on what actually goes out on the wire.
	var gotRaw, gotDecoded string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRaw = r.URL.EscapedPath()
		gotDecoded = r.URL.Path
		_, _ = w.Write([]byte(`{"deleted":true}`))
	}))
	defer srv.Close()

	if err := testClient(t, srv.URL).DeleteIPv4Rule(context.Background(), "allow lan to wan"); err != nil {
		t.Fatalf("DeleteIPv4Rule: %v", err)
	}
	if want := DefaultAPIPath + "/firewall/rules/ipv4/allow%20lan%20to%20wan"; gotRaw != want {
		t.Errorf("escaped path = %q, want %q", gotRaw, want)
	}
	if want := DefaultAPIPath + "/firewall/rules/ipv4/allow lan to wan"; gotDecoded != want {
		t.Errorf("decoded path = %q, want %q", gotDecoded, want)
	}
}

func TestRuleIdentifiersThatWouldLeaveThePathAreRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("request should not have been sent: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	c := testClient(t, srv.URL)
	ctx := context.Background()

	for _, bad := range []string{"", "   ", "a/b", "..", "."} {
		if err := c.DeleteIPv4Rule(ctx, bad); err == nil {
			t.Errorf("DeleteIPv4Rule(%q) should have been rejected", bad)
		}
		if _, err := c.UpdateIPv4Rule(ctx, bad, map[string]any{"logTraffic": true}); err == nil {
			t.Errorf("UpdateIPv4Rule(%q) should have been rejected", bad)
		}
		if _, err := c.GetIPv4Rule(ctx, bad); err == nil {
			t.Errorf("GetIPv4Rule(%q) should have been rejected", bad)
		}
	}
}
