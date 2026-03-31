package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
)

func doTokenJSONRequest(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reqBody []byte
	var err error
	if body != nil {
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}

	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func doTokenRequest(t *testing.T, handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func decodeProxyURLFromResponse(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()

	var body proxyURLResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v body=%q", err, rec.Body.String())
	}
	if body.ProxyURL == "" {
		t.Fatalf("proxy_url missing: body=%s", rec.Body.String())
	}
	parsed, err := url.Parse(body.ProxyURL)
	if err != nil {
		t.Fatalf("parse proxy_url %q: %v", body.ProxyURL, err)
	}
	return parsed
}

func TestTokenActionInheritLease_Success(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-target"
	platformID := mustCreatePlatform(t, srv, platformName)

	nowNs := time.Now().UnixNano()
	parent := model.Lease{
		PlatformID:     platformID,
		Account:        "parent-account",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.10",
		CreatedAtNs:    nowNs - int64(10*time.Minute),
		ExpiryNs:       nowNs + int64(30*time.Minute),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(parent); err != nil {
		t.Fatalf("seed parent lease: %v", err)
	}

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent-account",
			"new_account":    "new-account",
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := decodeJSONMap(t, rec)
	if body["status"] != "ok" {
		t.Fatalf("status field: got %v, want %q", body["status"], "ok")
	}

	child := cp.Router.ReadLease(model.LeaseKey{PlatformID: platformID, Account: "new-account"})
	if child == nil {
		t.Fatal("expected new-account lease to be created")
	}
	if child.NodeHash != parent.NodeHash {
		t.Fatalf("child node_hash: got %q, want %q", child.NodeHash, parent.NodeHash)
	}
	if child.EgressIP != parent.EgressIP {
		t.Fatalf("child egress_ip: got %q, want %q", child.EgressIP, parent.EgressIP)
	}
	if child.ExpiryNs != parent.ExpiryNs {
		t.Fatalf("child expiry_ns: got %d, want %d", child.ExpiryNs, parent.ExpiryNs)
	}
}

func TestTokenActionInheritLease_RejectsUnknownFields(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-unknown-field"
	_ = mustCreatePlatform(t, srv, platformName)

	handler := NewTokenActionHandler("tok", cp, 1<<20)
	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "parent",
			"new_account":    "child",
			"extra":          "unexpected",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestTokenActionGetProxy_SuccessUsesRequestHostPort(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-proxy-target"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	req := httptest.NewRequest(
		http.MethodGet,
		"http://sticky.example.com:9443/tok/api/v1/"+platformName+"/get-proxy",
		nil,
	)
	rec := doTokenRequest(t, handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	parsed := decodeProxyURLFromResponse(t, rec)
	if parsed.Scheme != "http" {
		t.Fatalf("scheme: got %q, want %q", parsed.Scheme, "http")
	}
	if parsed.Host != "sticky.example.com:9443" {
		t.Fatalf("host: got %q, want %q", parsed.Host, "sticky.example.com:9443")
	}
	if parsed.User == nil {
		t.Fatal("expected userinfo to be present")
	}
	if got := parsed.User.Username(); !regexp.MustCompile("^" + platformName + "\\.[0-9A-Za-z]{6}$").MatchString(got) {
		t.Fatalf("username: got %q, want %q + random suffix", got, platformName+".XXXXXX")
	}
	password, ok := parsed.User.Password()
	if !ok || password != "tok" {
		t.Fatalf("password: got %q (ok=%v), want %q", password, ok, "tok")
	}
}

func TestTokenActionGetProxy_UsesFallbackPortWhenRequestHostHasNoPort(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	cp.EnvCfg.ResinPort = 3128
	platformName := "token-proxy-no-port"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	req := httptest.NewRequest(
		http.MethodGet,
		"http://sticky.example.com/tok/api/v1/"+platformName+"/get-proxy",
		nil,
	)
	rec := doTokenRequest(t, handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	parsed := decodeProxyURLFromResponse(t, rec)
	if parsed.Host != "sticky.example.com:3128" {
		t.Fatalf("host: got %q, want %q", parsed.Host, "sticky.example.com:3128")
	}
}

func TestTokenActionGetProxy_FallsBackToListenAddressWhenRequestHostEmpty(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	cp.EnvCfg.ListenAddress = "10.0.0.9"
	cp.EnvCfg.ResinPort = 4321
	platformName := "token-proxy-fallback-host"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	req := httptest.NewRequest(
		http.MethodGet,
		"/tok/api/v1/"+platformName+"/get-proxy",
		nil,
	)
	req.Host = ""
	rec := doTokenRequest(t, handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	parsed := decodeProxyURLFromResponse(t, rec)
	if parsed.Host != "10.0.0.9:4321" {
		t.Fatalf("host: got %q, want %q", parsed.Host, "10.0.0.9:4321")
	}
}

func TestTokenActionGetProxy_PreservesIPv6HostFormatting(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-proxy-ipv6"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	req := httptest.NewRequest(
		http.MethodGet,
		"http://[2001:db8::1]:9443/tok/api/v1/"+platformName+"/get-proxy",
		nil,
	)
	rec := doTokenRequest(t, handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	parsed := decodeProxyURLFromResponse(t, rec)
	if parsed.Host != "[2001:db8::1]:9443" {
		t.Fatalf("host: got %q, want %q", parsed.Host, "[2001:db8::1]:9443")
	}
}

func TestTokenActionGetProxy_UnknownPlatformReturnsNotFound(t *testing.T) {
	_, cp, _ := newControlPlaneTestServer(t)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(t, handler, http.MethodGet, "/tok/api/v1/missing-platform/get-proxy", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")
}

func TestTokenActionGetProxy_BlankPlatformReturnsInvalidArgument(t *testing.T) {
	_, cp, _ := newControlPlaneTestServer(t)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(t, handler, http.MethodGet, "/tok/api/v1/%20%20/get-proxy", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestTokenActionGetProxy_EmptyProxyTokenReturnsPasswordlessURL(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-proxy-no-auth"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("", cp, 1<<20)

	req := httptest.NewRequest(
		http.MethodGet,
		"http://sticky.example.com:2260/any-token/api/v1/"+platformName+"/get-proxy",
		nil,
	)
	rec := doTokenRequest(t, handler, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	parsed := decodeProxyURLFromResponse(t, rec)
	if parsed.User == nil {
		t.Fatal("expected userinfo to be present")
	}
	if _, ok := parsed.User.Password(); ok {
		t.Fatalf("expected passwordless URL, got %q", parsed.String())
	}
	if got := parsed.User.Username(); !strings.HasPrefix(got, platformName+".") {
		t.Fatalf("username: got %q, want prefix %q", got, platformName+".")
	}
}

func TestTokenActionInheritLease_ParentMissingOrExpiredReturnsNotFound(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-parent-notfound"
	platformID := mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "missing-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")

	nowNs := time.Now().UnixNano()
	expired := model.Lease{
		PlatformID:     platformID,
		Account:        "expired-parent",
		NodeHash:       node.HashFromRawOptions([]byte(`{"id":"expired-token-parent-node"}`)).Hex(),
		EgressIP:       "203.0.113.22",
		CreatedAtNs:    nowNs - int64(2*time.Hour),
		ExpiryNs:       nowNs - int64(time.Second),
		LastAccessedNs: nowNs - int64(time.Minute),
	}
	if err := cp.Router.UpsertLease(expired); err != nil {
		t.Fatalf("seed expired lease: %v", err)
	}

	rec = doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "expired-parent",
			"new_account":    "child",
		},
	)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expired parent status: got %d, want %d, body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	assertErrorCode(t, rec, "NOT_FOUND")
}

func TestTokenActionInheritLease_InvalidArguments(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	platformName := "token-lease-invalid-args"
	_ = mustCreatePlatform(t, srv, platformName)
	handler := NewTokenActionHandler("tok", cp, 1<<20)

	rec := doTokenJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/tok/api/v1/"+platformName+"/actions/inherit-lease",
		map[string]any{
			"parent_account": "same-account",
			"new_account":    "same-account",
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("same account status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}
