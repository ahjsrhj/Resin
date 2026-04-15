package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/platform"
)

func TestAPIContract_SubscriptionLocalCreateValidation(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	rec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-local",
		"source_type": "local",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create local without content status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")

	rec = doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":        "sub-local",
		"source_type": "local",
		"content":     "vmess://example",
		"url":         "https://example.com/sub",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create local with url status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_SubscriptionSourceTypeReadOnlyOnPatch(t *testing.T) {
	srv, _, _ := newControlPlaneTestServer(t)

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name": "sub-remote",
		"url":  "https://example.com/sub",
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create remote subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	body := decodeJSONMap(t, createRec)
	subID, _ := body["id"].(string)
	if subID == "" {
		t.Fatalf("create remote subscription missing id: body=%s", createRec.Body.String())
	}

	rec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"source_type": "local",
	}, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch source_type status: got %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	assertErrorCode(t, rec, "INVALID_ARGUMENT")
}

func TestAPIContract_SubscriptionChainPlatformID_CreateAndPatch(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)
	chainPlatformID := "11111111-1111-1111-1111-111111111111"
	cp.Pool.RegisterPlatform(platform.NewConfiguredPlatform(
		chainPlatformID,
		"chain-platform",
		nil,
		nil,
		int64(time.Hour),
		string(platform.ReverseProxyMissActionTreatAsEmpty),
		string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		"",
		string(platform.AllocationPolicyBalanced),
	))

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":              "sub-chain",
		"url":               "https://example.com/sub",
		"chain_platform_id": chainPlatformID,
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	createBody := decodeJSONMap(t, createRec)
	if got := createBody["chain_platform_id"]; got != chainPlatformID {
		t.Fatalf("create chain_platform_id: got %v, want %q", got, chainPlatformID)
	}
	subID, _ := createBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}

	patchRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"chain_platform_id": "",
	}, true)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch subscription status: got %d, want %d, body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	patchBody := decodeJSONMap(t, patchRec)
	if got := patchBody["chain_platform_id"]; got != "" {
		t.Fatalf("patched chain_platform_id: got %v, want empty string", got)
	}

	getRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get subscription status: got %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	getBody := decodeJSONMap(t, getRec)
	if got := getBody["chain_platform_id"]; got != "" {
		t.Fatalf("stored chain_platform_id after clear: got %v, want empty string", got)
	}
}

func TestAPIContract_SubscriptionChainPlatformIDValidation(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	missingRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":              "sub-chain-missing",
		"url":               "https://example.com/sub",
		"chain_platform_id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, true)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid platform id create status: got %d, want %d, body=%s", missingRec.Code, http.StatusBadRequest, missingRec.Body.String())
	}
	assertErrorCode(t, missingRec, "INVALID_ARGUMENT")

	validButMissingID := "22222222-2222-2222-2222-222222222222"

	notReadyRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":              "sub-chain-missing-platform",
		"url":               "https://example.com/sub-2",
		"chain_platform_id": validButMissingID,
	}, true)
	if notReadyRec.Code != http.StatusBadRequest {
		t.Fatalf("missing platform create status: got %d, want %d, body=%s", notReadyRec.Code, http.StatusBadRequest, notReadyRec.Body.String())
	}
	assertErrorCode(t, notReadyRec, "INVALID_ARGUMENT")

	cp.Pool.RegisterPlatform(platform.NewConfiguredPlatform(
		validButMissingID,
		"chain-platform",
		nil,
		nil,
		int64(time.Hour),
		string(platform.ReverseProxyMissActionTreatAsEmpty),
		string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		"",
		string(platform.AllocationPolicyBalanced),
	))
}
