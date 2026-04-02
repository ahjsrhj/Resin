package service

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/config"
	"github.com/Resinat/Resin/internal/geoip"
	"github.com/Resinat/Resin/internal/model"
	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/probe"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/subscription"
	"github.com/Resinat/Resin/internal/testutil"
	"github.com/Resinat/Resin/internal/topology"
)

func newNodeListTestPool(subMgr *topology.SubscriptionManager) *topology.GlobalNodePool {
	return topology.NewGlobalNodePool(topology.PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "us" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return 10 * time.Minute },
	})
}

func addRoutableNodeForSubscription(
	t *testing.T,
	pool *topology.GlobalNodePool,
	sub *subscription.Subscription,
	raw []byte,
	egressIP string,
) node.Hash {
	return addRoutableNodeForSubscriptionWithTag(t, pool, sub, raw, egressIP, "tag")
}

func addRoutableNodeForSubscriptionWithTag(
	t *testing.T,
	pool *topology.GlobalNodePool,
	sub *subscription.Subscription,
	raw []byte,
	egressIP string,
	tag string,
) node.Hash {
	t.Helper()

	hash := node.HashFromRawOptions(raw)
	pool.AddNodeFromSub(hash, raw, sub.ID)
	sub.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{tag}})

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s not found after add", hash.Hex())
	}
	entry.SetEgressIP(netip.MustParseAddr(egressIP))
	if entry.LatencyTable == nil {
		t.Fatalf("node %s latency table not initialized", hash.Hex())
	}
	entry.LatencyTable.Update("example.com", 25*time.Millisecond, 10*time.Minute)
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	pool.RecordResult(hash, true)
	pool.NotifyNodeDirty(hash)
	return hash
}

func TestListNodes_PlatformAndSubscriptionFiltersReturnIntersection(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	plat := platform.NewPlatform("plat-1", "plat", nil, nil)
	pool.RegisterPlatform(plat)

	subA := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subB := subscription.NewSubscription("sub-b", "sub-b", "https://example.com/b", true, false)
	subMgr.Register(subA)
	subMgr.Register(subB)

	hashA := addRoutableNodeForSubscription(
		t,
		pool,
		subA,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.10",
	)
	_ = addRoutableNodeForSubscription(
		t,
		pool,
		subB,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.11",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}
	filters := NodeFilters{
		PlatformID:     &plat.ID,
		SubscriptionID: &subA.ID,
	}

	nodes, err := cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("intersection size = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != hashA.Hex() {
		t.Fatalf("intersection node hash = %q, want %q", nodes[0].NodeHash, hashA.Hex())
	}
}

func TestListNodes_SubscriptionFilterSkipsStaleManagedNodes(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	staleHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"9.9.9.9","port":443}`))
	sub.ManagedNodes().StoreNode(staleHash, subscription.ManagedNode{Tags: []string{"stale"}})

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}
	filters := NodeFilters{
		SubscriptionID: &sub.ID,
	}

	nodes, err := cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes with stale hash: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("nodes with stale managed hash = %d, want 0", len(nodes))
	}

	liveHash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.20",
	)

	nodes, err = cp.ListNodes(filters)
	if err != nil {
		t.Fatalf("ListNodes with stale+live hashes: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes with stale+live hashes = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != liveHash.Hex() {
		t.Fatalf("live node hash = %q, want %q", nodes[0].NodeHash, liveHash.Hex())
	}
}

func TestListNodes_SubscriptionFilterSkipsEvictedManagedNodes(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	subA := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subB := subscription.NewSubscription("sub-b", "sub-b", "https://example.com/b", true, false)
	subMgr.Register(subA)
	subMgr.Register(subB)

	raw := []byte(`{"type":"ss","server":"7.7.7.7","port":443}`)
	hash := addRoutableNodeForSubscriptionWithTag(t, pool, subA, raw, "203.0.113.40", "a-tag")
	pool.AddNodeFromSub(hash, raw, subB.ID)
	subB.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"b-tag"}})

	managedA, ok := subA.ManagedNodes().LoadNode(hash)
	if !ok {
		t.Fatal("subA managed node missing before eviction")
	}
	managedA.Evicted = true
	subA.ManagedNodes().StoreNode(hash, managedA)
	pool.RemoveNodeFromSub(hash, subA.ID)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	filtersA := NodeFilters{SubscriptionID: &subA.ID}
	nodesA, err := cp.ListNodes(filtersA)
	if err != nil {
		t.Fatalf("ListNodes subA: %v", err)
	}
	if len(nodesA) != 0 {
		t.Fatalf("subA filtered nodes = %d, want 0", len(nodesA))
	}

	filtersB := NodeFilters{SubscriptionID: &subB.ID}
	nodesB, err := cp.ListNodes(filtersB)
	if err != nil {
		t.Fatalf("ListNodes subB: %v", err)
	}
	if len(nodesB) != 1 || nodesB[0].NodeHash != hash.Hex() {
		t.Fatalf("subB filtered nodes = %+v, want [%s]", nodesB, hash.Hex())
	}
}

func TestGetNode_TagIncludesSubscriptionNamePrefix(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if len(got.Tags) != 1 {
		t.Fatalf("tags len = %d, want 1", len(got.Tags))
	}
	if got.Tags[0].Tag != "sub-a/tag" {
		t.Fatalf("tag = %q, want %q", got.Tags[0].Tag, "sub-a/tag")
	}
}

func TestGetNode_ReferenceLatencyMsUsesAuthorityAverage(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}
	entry.LatencyTable.LoadEntry("cloudflare.com", node.DomainLatencyStats{
		Ewma:        40 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("github.com", node.DomainLatencyStats{
		Ewma:        60 * time.Millisecond,
		LastUpdated: time.Now(),
	})
	entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
		Ewma:        5 * time.Millisecond,
		LastUpdated: time.Now(),
	})

	runtimeCfg := &atomic.Pointer[config.RuntimeConfig]{}
	cfg := config.NewDefaultRuntimeConfig()
	cfg.LatencyAuthorities = []string{"cloudflare.com", "github.com", "google.com"}
	runtimeCfg.Store(cfg)

	cp := &ControlPlaneService{
		Pool:       pool,
		SubMgr:     subMgr,
		GeoIP:      &geoip.Service{},
		RuntimeCfg: runtimeCfg,
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.ReferenceLatencyMs == nil {
		t.Fatal("reference_latency_ms should be present")
	}
	if *got.ReferenceLatencyMs != 50 {
		t.Fatalf("reference_latency_ms = %v, want 50", *got.ReferenceLatencyMs)
	}
}

func TestListNodes_ProbedSinceUsesLastLatencyProbeAttempt(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}

	latencyAttempt := time.Now().Add(-2 * time.Minute).UnixNano()
	entry.LastLatencyProbeAttempt.Store(latencyAttempt)
	// Keep egress update older to ensure filter is using LastLatencyProbeAttempt.
	entry.LastEgressUpdate.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	before := time.Unix(0, latencyAttempt).Add(-1 * time.Minute)
	nodes, err := cp.ListNodes(NodeFilters{ProbedSince: &before})
	if err != nil {
		t.Fatalf("ListNodes(before): %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes(before) len = %d, want 1", len(nodes))
	}

	after := time.Unix(0, latencyAttempt).Add(1 * time.Minute)
	nodes, err = cp.ListNodes(NodeFilters{ProbedSince: &after})
	if err != nil {
		t.Fatalf("ListNodes(after): %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("ListNodes(after) len = %d, want 0", len(nodes))
	}
}

func TestListNodes_TagKeywordFuzzyMatchIsCaseInsensitive(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	matchHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.30",
		"hongkong-fast-01",
	)
	_ = addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.31",
		"japan-slow-01",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	keyword := "FAST"
	nodes, err := cp.ListNodes(NodeFilters{TagKeyword: &keyword})
	if err != nil {
		t.Fatalf("ListNodes(tag_keyword): %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("ListNodes(tag_keyword) len = %d, want 1", len(nodes))
	}
	if nodes[0].NodeHash != matchHash.Hex() {
		t.Fatalf("ListNodes(tag_keyword) hash = %q, want %q", nodes[0].NodeHash, matchHash.Hex())
	}
}

func TestListNodes_RegionFilterAndSummaryPreferStoredRegion(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.40",
	)

	entry, ok := pool.GetEntry(hash)
	if !ok {
		t.Fatalf("node %s missing", hash.Hex())
	}
	entry.SetEgressRegion("jp")

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{}, // empty service returns "", forcing stored-region path
	}

	region := "jp"
	nodes, err := cp.ListNodes(NodeFilters{Region: &region})
	if err != nil {
		t.Fatalf("ListNodes(region): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != hash.Hex() {
		t.Fatalf("region-filtered nodes = %+v, want [%s]", nodes, hash.Hex())
	}

	got, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got.Region != "jp" {
		t.Fatalf("summary region: got %q, want %q", got.Region, "jp")
	}
}

func TestListNodes_EnabledFilter(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	subEnabled := subscription.NewSubscription("sub-enabled", "sub-enabled", "https://example.com/enabled", true, false)
	subDisabled := subscription.NewSubscription("sub-disabled", "sub-disabled", "https://example.com/disabled", false, false)
	subMgr.Register(subEnabled)
	subMgr.Register(subDisabled)

	enabledHash := addRoutableNodeForSubscription(
		t,
		pool,
		subEnabled,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.70",
	)
	disabledHash := addRoutableNodeForSubscription(
		t,
		pool,
		subDisabled,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.71",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	enabled := true
	nodes, err := cp.ListNodes(NodeFilters{Enabled: &enabled})
	if err != nil {
		t.Fatalf("ListNodes(enabled=true): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != enabledHash.Hex() {
		t.Fatalf("enabled filter result = %+v, want [%s]", nodes, enabledHash.Hex())
	}

	disabled := false
	nodes, err = cp.ListNodes(NodeFilters{Enabled: &disabled})
	if err != nil {
		t.Fatalf("ListNodes(enabled=false): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != disabledHash.Hex() {
		t.Fatalf("disabled filter result = %+v, want [%s]", nodes, disabledHash.Hex())
	}
}

func TestListNodes_SubscriptionNodeEnabledFilter(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	enabledHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.80",
		"enabled",
	)
	disabledHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"2.2.2.2","port":443}`),
		"203.0.113.81",
		"disabled",
	)
	managed, ok := sub.ManagedNodes().LoadNode(disabledHash)
	if !ok {
		t.Fatal("disabled managed node missing")
	}
	managed.Disabled = true
	sub.ManagedNodes().StoreNode(disabledHash, managed)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	enabled := true
	nodes, err := cp.ListNodes(NodeFilters{
		SubscriptionID:          &sub.ID,
		SubscriptionNodeEnabled: &enabled,
	})
	if err != nil {
		t.Fatalf("ListNodes(subscription_node_enabled=true): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != enabledHash.Hex() {
		t.Fatalf("subscription_node_enabled=true result = %+v, want [%s]", nodes, enabledHash.Hex())
	}

	disabled := false
	nodes, err = cp.ListNodes(NodeFilters{
		SubscriptionID:          &sub.ID,
		SubscriptionNodeEnabled: &disabled,
	})
	if err != nil {
		t.Fatalf("ListNodes(subscription_node_enabled=false): %v", err)
	}
	if len(nodes) != 1 || nodes[0].NodeHash != disabledHash.Hex() {
		t.Fatalf("subscription_node_enabled=false result = %+v, want [%s]", nodes, disabledHash.Hex())
	}
}

func TestSetSubscriptionNodeDisabled_TogglesBindingAndGlobalState(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	subA := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subB := subscription.NewSubscription("sub-b", "sub-b", "https://example.com/b", true, false)
	subMgr.Register(subA)
	subMgr.Register(subB)

	raw := []byte(`{"type":"ss","server":"4.4.4.4","port":443}`)
	hash := addRoutableNodeForSubscriptionWithTag(t, pool, subA, raw, "203.0.113.82", "a-tag")
	pool.AddNodeFromSub(hash, raw, subB.ID)
	subB.ManagedNodes().StoreNode(hash, subscription.ManagedNode{Tags: []string{"b-tag"}})

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	if err := cp.SetSubscriptionNodeDisabled(subA.ID, hash.Hex(), true); err != nil {
		t.Fatalf("disable subA binding: %v", err)
	}
	if pool.IsNodeDisabled(hash) {
		t.Fatal("node should remain globally enabled while subB binding is active")
	}

	summary, err := cp.GetNode(hash.Hex())
	if err != nil {
		t.Fatalf("GetNode after disable: %v", err)
	}
	tagStates := map[string]NodeTag{}
	for _, tag := range summary.Tags {
		tagStates[tag.SubscriptionID] = tag
	}
	if !tagStates[subA.ID].Disabled || tagStates[subA.ID].SubscriptionEnabled {
		t.Fatalf("subA tag state mismatch: %+v", tagStates[subA.ID])
	}
	if tagStates[subB.ID].Disabled || !tagStates[subB.ID].SubscriptionEnabled {
		t.Fatalf("subB tag state mismatch: %+v", tagStates[subB.ID])
	}

	if err := cp.SetSubscriptionNodeDisabled(subB.ID, hash.Hex(), true); err != nil {
		t.Fatalf("disable subB binding: %v", err)
	}
	if !pool.IsNodeDisabled(hash) {
		t.Fatal("node should become globally disabled when all bindings are disabled")
	}

	if err := cp.SetSubscriptionNodeDisabled(subA.ID, hash.Hex(), false); err != nil {
		t.Fatalf("re-enable subA binding: %v", err)
	}
	if pool.IsNodeDisabled(hash) {
		t.Fatal("node should recover once one binding is re-enabled")
	}
}

func TestSetSubscriptionNodeDisabled_Errors(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	raw := []byte(`{"type":"ss","server":"5.5.5.5","port":443}`)
	hash := addRoutableNodeForSubscriptionWithTag(t, pool, sub, raw, "203.0.113.83", "tag")

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{},
	}

	if err := cp.SetSubscriptionNodeDisabled(sub.ID, "not-hex", true); err == nil {
		t.Fatal("invalid hash should fail")
	}
	if err := cp.SetSubscriptionNodeDisabled("missing-sub", hash.Hex(), true); err == nil {
		t.Fatal("missing subscription should fail")
	}

	missingHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"6.6.6.6","port":443}`))
	if err := cp.SetSubscriptionNodeDisabled(sub.ID, missingHash.Hex(), true); err == nil {
		t.Fatal("missing subscription node should fail")
	}

	managed, ok := sub.ManagedNodes().LoadNode(hash)
	if !ok {
		t.Fatal("managed node missing")
	}
	managed.Evicted = true
	sub.ManagedNodes().StoreNode(hash, managed)
	pool.RemoveNodeFromSub(hash, sub.ID)

	if err := cp.SetSubscriptionNodeDisabled(sub.ID, hash.Hex(), true); err == nil {
		t.Fatal("evicted subscription node should fail")
	}
}

func TestSetSubscriptionNodeDisabled_ReconcilesLeaseToReplacement(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	plat := platform.NewPlatform("plat-lease-reconcile", "plat-lease-reconcile", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	oldHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"10.0.0.1","port":443}`),
		"203.0.113.90",
		"old-tag",
	)
	newHash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"10.0.0.2","port":443}`),
		"203.0.113.91",
		"new-tag",
	)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:            pool,
		Authorities:     func() []string { return []string{"example.com"} },
		P2CWindow:       func() time.Duration { return 10 * time.Minute },
		NodeTagResolver: pool.ResolveNodeDisplayTag,
	})
	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		Router: router,
		GeoIP:  &geoip.Service{},
	}

	now := time.Now().UnixNano()
	if err := cp.Router.UpsertLease(model.Lease{
		PlatformID:     plat.ID,
		Account:        "alice",
		NodeHash:       oldHash.Hex(),
		EgressIP:       "203.0.113.90",
		CreatedAtNs:    now - int64(time.Minute),
		ExpiryNs:       now + int64(time.Hour),
		LastAccessedNs: now,
	}); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}

	if err := cp.SetSubscriptionNodeDisabled(sub.ID, oldHash.Hex(), true); err != nil {
		t.Fatalf("SetSubscriptionNodeDisabled: %v", err)
	}

	lease := cp.Router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "alice"})
	if lease == nil {
		t.Fatal("expected lease to be migrated to replacement node")
	}
	if lease.NodeHash != newHash.Hex() {
		t.Fatalf("lease node_hash: got %q, want %q", lease.NodeHash, newHash.Hex())
	}

	gotLease, err := cp.GetLease(plat.ID, "alice")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if gotLease.NodeHash != newHash.Hex() {
		t.Fatalf("GetLease node_hash: got %q, want %q", gotLease.NodeHash, newHash.Hex())
	}

	leases, err := cp.ListLeases(plat.ID)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 {
		t.Fatalf("ListLeases len: got %d, want 1", len(leases))
	}
	if leases[0].NodeHash != newHash.Hex() {
		t.Fatalf("ListLeases node_hash: got %q, want %q", leases[0].NodeHash, newHash.Hex())
	}
}

func TestSetSubscriptionNodeDisabled_RemovesLeaseWithoutReplacement(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)
	plat := platform.NewPlatform("plat-lease-remove", "plat-lease-remove", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.RegisterPlatform(plat)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscriptionWithTag(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"10.0.0.3","port":443}`),
		"203.0.113.92",
		"only-tag",
	)

	router := routing.NewRouter(routing.RouterConfig{
		Pool:            pool,
		Authorities:     func() []string { return []string{"example.com"} },
		P2CWindow:       func() time.Duration { return 10 * time.Minute },
		NodeTagResolver: pool.ResolveNodeDisplayTag,
	})
	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		Router: router,
		GeoIP:  &geoip.Service{},
	}

	now := time.Now().UnixNano()
	if err := cp.Router.UpsertLease(model.Lease{
		PlatformID:     plat.ID,
		Account:        "bob",
		NodeHash:       hash.Hex(),
		EgressIP:       "203.0.113.92",
		CreatedAtNs:    now - int64(time.Minute),
		ExpiryNs:       now + int64(time.Hour),
		LastAccessedNs: now,
	}); err != nil {
		t.Fatalf("UpsertLease: %v", err)
	}

	if err := cp.SetSubscriptionNodeDisabled(sub.ID, hash.Hex(), true); err != nil {
		t.Fatalf("SetSubscriptionNodeDisabled: %v", err)
	}

	if lease := cp.Router.ReadLease(model.LeaseKey{PlatformID: plat.ID, Account: "bob"}); lease != nil {
		t.Fatalf("expected lease removal, got %+v", lease)
	}

	if _, err := cp.GetLease(plat.ID, "bob"); err == nil {
		t.Fatal("expected GetLease to fail after lease removal")
	} else {
		assertServiceErrorCode(t, err, "NOT_FOUND")
	}

	leases, err := cp.ListLeases(plat.ID)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 0 {
		t.Fatalf("ListLeases len: got %d, want 0", len(leases))
	}
}

func TestProbeEgress_ReturnsRegion(t *testing.T) {
	subMgr := topology.NewSubscriptionManager()
	pool := newNodeListTestPool(subMgr)

	sub := subscription.NewSubscription("sub-a", "sub-a", "https://example.com/a", true, false)
	subMgr.Register(sub)

	hash := addRoutableNodeForSubscription(
		t,
		pool,
		sub,
		[]byte(`{"type":"ss","server":"1.1.1.1","port":443}`),
		"203.0.113.60",
	)

	cp := &ControlPlaneService{
		Pool:   pool,
		SubMgr: subMgr,
		GeoIP:  &geoip.Service{}, // empty service keeps focus on stored region from loc
		ProbeMgr: probe.NewProbeManager(probe.ProbeConfig{
			Pool: pool,
			Fetcher: func(_ node.Hash, _ string) ([]byte, time.Duration, error) {
				return []byte("ip=198.51.100.88\nloc=JP"), 20 * time.Millisecond, nil
			},
		}),
	}

	got, err := cp.ProbeEgress(hash.Hex())
	if err != nil {
		t.Fatalf("ProbeEgress: %v", err)
	}
	if got.EgressIP != "198.51.100.88" {
		t.Fatalf("egress_ip: got %q, want %q", got.EgressIP, "198.51.100.88")
	}
	if got.Region != "jp" {
		t.Fatalf("region: got %q, want %q", got.Region, "jp")
	}
}
