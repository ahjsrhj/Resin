package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/testutil"
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

func TestAPIContract_SubscriptionChainNodeHash_CreateAndPatch(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	raw := []byte(`{"type":"ss","server":"1.1.1.1","port":443}`)
	hash := node.HashFromRawOptions(raw)
	entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
	outbound := testutil.NewNoopOutbound()
	entry.Outbound.Store(&outbound)
	cp.Pool.LoadNodeFromBootstrap(entry)

	createRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":            "sub-chain",
		"url":             "https://example.com/sub",
		"chain_node_hash": hash.Hex(),
	}, true)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create subscription status: got %d, want %d, body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}
	createBody := decodeJSONMap(t, createRec)
	if got := createBody["chain_node_hash"]; got != hash.Hex() {
		t.Fatalf("create chain_node_hash: got %v, want %q", got, hash.Hex())
	}
	subID, _ := createBody["id"].(string)
	if subID == "" {
		t.Fatalf("create subscription missing id: body=%s", createRec.Body.String())
	}

	patchRec := doJSONRequest(t, srv, http.MethodPatch, "/api/v1/subscriptions/"+subID, map[string]any{
		"chain_node_hash": "",
	}, true)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch subscription status: got %d, want %d, body=%s", patchRec.Code, http.StatusOK, patchRec.Body.String())
	}
	patchBody := decodeJSONMap(t, patchRec)
	if got := patchBody["chain_node_hash"]; got != "" {
		t.Fatalf("patched chain_node_hash: got %v, want empty string", got)
	}

	getRec := doJSONRequest(t, srv, http.MethodGet, "/api/v1/subscriptions/"+subID, nil, true)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get subscription status: got %d, want %d, body=%s", getRec.Code, http.StatusOK, getRec.Body.String())
	}
	getBody := decodeJSONMap(t, getRec)
	if got := getBody["chain_node_hash"]; got != "" {
		t.Fatalf("stored chain_node_hash after clear: got %v, want empty string", got)
	}
}

func TestAPIContract_SubscriptionChainNodeHashValidation(t *testing.T) {
	srv, cp, _ := newControlPlaneTestServer(t)

	missingRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":            "sub-chain-missing",
		"url":             "https://example.com/sub",
		"chain_node_hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, true)
	if missingRec.Code != http.StatusBadRequest {
		t.Fatalf("missing node create status: got %d, want %d, body=%s", missingRec.Code, http.StatusBadRequest, missingRec.Body.String())
	}
	assertErrorCode(t, missingRec, "INVALID_ARGUMENT")

	raw := []byte(`{"type":"ss","server":"1.1.1.2","port":443}`)
	hash := node.HashFromRawOptions(raw)
	cp.Pool.LoadNodeFromBootstrap(node.NewNodeEntry(hash, raw, time.Now().Add(time.Second), 16))

	notReadyRec := doJSONRequest(t, srv, http.MethodPost, "/api/v1/subscriptions", map[string]any{
		"name":            "sub-chain-not-ready",
		"url":             "https://example.com/sub-2",
		"chain_node_hash": hash.Hex(),
	}, true)
	if notReadyRec.Code != http.StatusBadRequest {
		t.Fatalf("not ready create status: got %d, want %d, body=%s", notReadyRec.Code, http.StatusBadRequest, notReadyRec.Body.String())
	}
	assertErrorCode(t, notReadyRec, "INVALID_ARGUMENT")
}
