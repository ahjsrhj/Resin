package routing

import (
	"encoding/json"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/testutil"
)

type routerTestPool struct {
	mu          sync.RWMutex
	entries     map[node.Hash]*node.NodeEntry
	platsByID   map[string]*platform.Platform
	platsByName map[string]*platform.Platform
}

func newRouterTestPool() *routerTestPool {
	return &routerTestPool{
		entries:     make(map[node.Hash]*node.NodeEntry),
		platsByID:   make(map[string]*platform.Platform),
		platsByName: make(map[string]*platform.Platform),
	}
}

func (p *routerTestPool) addPlatform(plat *platform.Platform) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.platsByID[plat.ID] = plat
	p.platsByName[plat.Name] = plat
}

func (p *routerTestPool) addEntry(h node.Hash, entry *node.NodeEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.entries[h] = entry
}

func (p *routerTestPool) removeEntry(h node.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, h)
}

func (p *routerTestPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e, ok := p.entries[hash]
	return e, ok
}

func (p *routerTestPool) GetPlatform(id string) (*platform.Platform, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	plat, ok := p.platsByID[id]
	return plat, ok
}

func (p *routerTestPool) GetPlatformByName(name string) (*platform.Platform, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	plat, ok := p.platsByName[name]
	return plat, ok
}

func (p *routerTestPool) RangePlatforms(fn func(*platform.Platform) bool) {
	p.mu.RLock()
	plats := make([]*platform.Platform, 0, len(p.platsByID))
	for _, plat := range p.platsByID {
		plats = append(plats, plat)
	}
	p.mu.RUnlock()

	for _, plat := range plats {
		if !fn(plat) {
			return
		}
	}
}

func (p *routerTestPool) rebuildPlatformView(plat *platform.Platform) {
	p.mu.RLock()
	snapshot := make(map[node.Hash]*node.NodeEntry, len(p.entries))
	for h, e := range p.entries {
		snapshot[h] = e
	}
	p.mu.RUnlock()

	plat.FullRebuild(
		func(fn func(node.Hash, *node.NodeEntry) bool) {
			for h, e := range snapshot {
				if !fn(h, e) {
					return
				}
			}
		},
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)
}

func newRoutableEntry(t *testing.T, raw, ip string) (node.Hash, *node.NodeEntry) {
	t.Helper()
	rawOpts := json.RawMessage(raw)
	h := node.HashFromRawOptions(rawOpts)
	e := node.NewNodeEntry(h, rawOpts, time.Now(), 16)
	// Empty platform regex still requires at least one enabled subscription.
	e.AddSubscriptionID("sub-test")

	parsedIP, err := netip.ParseAddr(ip)
	if err != nil {
		t.Fatalf("parse ip %q: %v", ip, err)
	}
	e.SetEgressIP(parsedIP)

	// Keep at least one latency sample so the node remains routable.
	e.LatencyTable.Update("cloudflare.com", 100*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, e, "cloudflare.com")

	// Any non-nil outbound value is enough for platform filtering.
	ob := testutil.NewNoopOutbound()
	e.Outbound.Store(&ob)

	return h, e
}

func waitForDomainLatency(t *testing.T, e *node.NodeEntry, domain string) {
	t.Helper()
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if e.LatencyTable != nil {
			if _, ok := e.LatencyTable.GetDomainStats(domain); ok {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	if e.LatencyTable == nil {
		t.Fatalf("latency sample for domain %q did not become visible: latency table is nil", domain)
	}
	t.Fatalf(
		"latency sample for domain %q did not become visible in time: table size=%d",
		domain,
		e.LatencyTable.Size(),
	)
}

func newTestRouter(pool PoolAccessor, onEvent LeaseEventFunc) *Router {
	return NewRouter(RouterConfig{
		Pool:         pool,
		Authorities:  func() []string { return []string{"cloudflare.com"} },
		P2CWindow:    func() time.Duration { return 10 * time.Minute },
		OnLeaseEvent: onEvent,
	})
}

func TestRouteRequest_SameIPRotationPrefersTargetLatencySample(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-1", "Plat-1", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"current"}`, "198.51.100.77")
	candidateA, entryA := newRoutableEntry(t, `{"id":"candidate-a"}`, "198.51.100.77")
	candidateB, entryB := newRoutableEntry(t, `{"id":"candidate-b"}`, "198.51.100.77")
	pool.addEntry(currentHash, currentEntry)
	pool.addEntry(candidateA, entryA)
	pool.addEntry(candidateB, entryB)
	pool.rebuildPlatformView(plat)
	if !plat.View().Contains(currentHash) || !plat.View().Contains(candidateA) || !plat.View().Contains(candidateB) {
		t.Fatalf(
			"rebuild did not include expected nodes: size=%d current(lat=%v,out=%v) a(lat=%v,out=%v) b(lat=%v,out=%v)",
			plat.View().Size(),
			currentEntry.HasLatency(), currentEntry.HasOutbound(),
			entryA.HasLatency(), entryA.HasOutbound(),
			entryB.HasLatency(), entryB.HasOutbound(),
		)
	}

	// Force lease invalidation while keeping same-IP candidates in view.
	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	order := make([]node.Hash, 0, 2)
	plat.View().Range(func(h node.Hash) bool {
		if h == candidateA || h == candidateB {
			order = append(order, h)
		}
		return true
	})
	if len(order) != 2 {
		t.Fatalf("expected 2 same-ip candidates in view, got %d", len(order))
	}

	entries := map[node.Hash]*node.NodeEntry{
		candidateA: entryA,
		candidateB: entryB,
	}
	noSampleHash := order[0]
	preferredHash := order[1]
	entries[preferredHash].LatencyTable.Update("example.com", 20*time.Millisecond, 10*time.Minute)
	waitForDomainLatency(t, entries[preferredHash], "example.com")
	_ = noSampleHash // intentionally keep target-domain latency empty

	router := newTestRouter(pool, nil)
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	originalLease := Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       time.Now().Add(time.Hour).UnixNano(),
		LastAccessedNs: time.Now().UnixNano(),
	}
	state.Leases.CreateLease("acct-rotation", originalLease)

	res, err := router.RouteRequest(plat.Name, "acct-rotation", "https://example.com/path")
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if res.NodeHash != preferredHash {
		t.Fatalf("rotation picked %s, want %s (candidate with target-domain latency)", res.NodeHash.Hex(), preferredHash.Hex())
	}
	if res.LeaseCreated {
		t.Fatal("same-ip rotation should update existing lease, not create a new one")
	}

	updatedLease, ok := state.Leases.GetLease("acct-rotation")
	if !ok {
		t.Fatal("lease should still exist after rotation")
	}
	if updatedLease.NodeHash != preferredHash {
		t.Fatalf("lease node hash = %s, want %s", updatedLease.NodeHash.Hex(), preferredHash.Hex())
	}
	if updatedLease.ExpiryNs != originalLease.ExpiryNs {
		t.Fatalf("same-ip rotation must not change expiry: got %d want %d", updatedLease.ExpiryNs, originalLease.ExpiryNs)
	}
}

func TestRouteRequest_SelectedNodeRemovedAfterPick_EmitsLeaseRemove(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-2", "Plat-2", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	selectedHash, selectedEntry := newRoutableEntry(t, `{"id":"selected"}`, "203.0.113.20")
	pool.addEntry(selectedHash, selectedEntry)
	pool.rebuildPlatformView(plat)
	if !plat.View().Contains(selectedHash) {
		t.Fatalf(
			"setup expected selected hash in view: size=%d hasLatency=%v hasOutbound=%v",
			plat.View().Size(),
			selectedEntry.HasLatency(),
			selectedEntry.HasOutbound(),
		)
	}

	// Simulate stale view: node stays in platform view but has been removed from pool.
	pool.removeEntry(selectedHash)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	oldIP := netip.MustParseAddr("203.0.113.9")
	oldHash := node.HashFromRawOptions(json.RawMessage(`{"id":"old-lease-node"}`))
	state.Leases.CreateLease("acct-race", Lease{
		NodeHash:       oldHash,
		EgressIP:       oldIP,
		ExpiryNs:       time.Now().Add(time.Hour).UnixNano(),
		LastAccessedNs: time.Now().UnixNano(),
	})

	if got := state.IPLoadStats.Get(oldIP); got != 1 {
		t.Fatalf("setup ip load: got %d, want 1", got)
	}

	_, err := router.RouteRequest(plat.Name, "acct-race", "https://example.com")
	if err == nil {
		t.Fatal("expected route error when selected node disappears")
	}
	if !errors.Is(err, ErrNoAvailableNodes) {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := state.Leases.GetLease("acct-race"); ok {
		t.Fatal("lease should be removed when re-route fails after invalidation")
	}
	if got := state.IPLoadStats.Get(oldIP); got != 0 {
		t.Fatalf("ip load should decrement exactly once, got %d", got)
	}

	foundRemove := false
	for _, e := range events {
		if e.Type == LeaseRemove && e.Account == "acct-race" && e.NodeHash == oldHash && e.EgressIP == oldIP {
			foundRemove = true
			break
		}
	}
	if !foundRemove {
		t.Fatal("expected LeaseRemove event when old lease is dropped")
	}
}

func TestReconcileLeasesForNode_PrefersSameIPRotation(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-reconcile-same-ip", "Plat-Reconcile-Same-IP", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"reconcile-current"}`, "198.51.100.77")
	candidateA, entryA := newRoutableEntry(t, `{"id":"reconcile-a"}`, "198.51.100.77")
	candidateB, entryB := newRoutableEntry(t, `{"id":"reconcile-b"}`, "198.51.100.77")
	entryA.LatencyTable.Update("cloudflare.com", 80*time.Millisecond, 10*time.Minute)
	entryB.LatencyTable.Update("cloudflare.com", 20*time.Millisecond, 10*time.Minute)
	pool.addEntry(currentHash, currentEntry)
	pool.addEntry(candidateA, entryA)
	pool.addEntry(candidateB, entryB)
	pool.rebuildPlatformView(plat)

	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	oldExpiry := time.Now().Add(time.Hour).UnixNano()
	state.Leases.CreateLease("acct-same-ip", Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       oldExpiry,
		LastAccessedNs: time.Now().UnixNano(),
	})

	router.ReconcileLeasesForNode(currentHash)

	lease, ok := state.Leases.GetLease("acct-same-ip")
	if !ok {
		t.Fatal("expected lease to remain after same-ip reconciliation")
	}
	if lease.NodeHash != candidateB {
		t.Fatalf("same-ip reconciliation picked %s, want %s", lease.NodeHash.Hex(), candidateB.Hex())
	}
	if lease.ExpiryNs != oldExpiry {
		t.Fatalf("same-ip reconciliation must preserve expiry: got %d want %d", lease.ExpiryNs, oldExpiry)
	}
	if got := state.IPLoadStats.Get(currentEntry.GetEgressIP()); got != 1 {
		t.Fatalf("same-ip reconciliation should preserve IP load count, got %d", got)
	}

	if len(events) != 1 || events[0].Type != LeaseReplace || events[0].NodeHash != candidateB {
		t.Fatalf("unexpected lease events: %+v", events)
	}
}

func TestReconcileLeasesForNode_RecreatesLeaseOnOtherIP(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-reconcile-recreate", "Plat-Reconcile-Recreate", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"recreate-current"}`, "203.0.113.60")
	replacementHash, replacementEntry := newRoutableEntry(t, `{"id":"recreate-replacement"}`, "203.0.113.61")
	pool.addEntry(currentHash, currentEntry)
	pool.addEntry(replacementHash, replacementEntry)
	pool.rebuildPlatformView(plat)

	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	oldExpiry := time.Now().Add(time.Hour).UnixNano()
	oldLease := Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       oldExpiry,
		LastAccessedNs: time.Now().UnixNano(),
	}
	state.Leases.CreateLease("acct-recreate", oldLease)

	router.ReconcileLeasesForNode(currentHash)

	lease, ok := state.Leases.GetLease("acct-recreate")
	if !ok {
		t.Fatal("expected recreated lease to exist")
	}
	if lease.NodeHash != replacementHash {
		t.Fatalf("recreated lease hash = %s, want %s", lease.NodeHash.Hex(), replacementHash.Hex())
	}
	if lease.ExpiryNs == oldExpiry {
		t.Fatalf("recreated lease must get a new expiry, still got %d", oldExpiry)
	}
	if got := state.IPLoadStats.Get(oldLease.EgressIP); got != 0 {
		t.Fatalf("old IP load should be cleared, got %d", got)
	}
	if got := state.IPLoadStats.Get(replacementEntry.GetEgressIP()); got != 1 {
		t.Fatalf("replacement IP load should be 1, got %d", got)
	}

	foundRemove := false
	foundCreate := false
	for _, e := range events {
		if e.Type == LeaseRemove && e.Account == "acct-recreate" && e.NodeHash == currentHash {
			foundRemove = true
		}
		if e.Type == LeaseCreate && e.Account == "acct-recreate" && e.NodeHash == replacementHash {
			foundCreate = true
		}
	}
	if !foundRemove || !foundCreate {
		t.Fatalf("expected remove+create events, got %+v", events)
	}
}

func TestReconcileLeasesForNode_RemovesLeaseWhenNoReplacementExists(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-reconcile-delete", "Plat-Reconcile-Delete", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"delete-current"}`, "203.0.113.70")
	pool.addEntry(currentHash, currentEntry)
	pool.rebuildPlatformView(plat)

	currentEntry.CircuitOpenSince.Store(time.Now().UnixNano())
	plat.NotifyDirty(
		currentHash,
		pool.GetEntry,
		func(_ string, _ node.Hash) (string, bool, []string, bool) { return "", true, nil, true },
		func(_ netip.Addr) string { return "" },
	)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	state.Leases.CreateLease("acct-delete", Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       time.Now().Add(time.Hour).UnixNano(),
		LastAccessedNs: time.Now().UnixNano(),
	})

	router.ReconcileLeasesForNode(currentHash)

	if _, ok := state.Leases.GetLease("acct-delete"); ok {
		t.Fatal("expected lease to be removed when no replacement exists")
	}
	if got := state.IPLoadStats.Get(currentEntry.GetEgressIP()); got != 0 {
		t.Fatalf("deleted lease should clear IP load, got %d", got)
	}
	if len(events) != 1 || events[0].Type != LeaseRemove || events[0].NodeHash != currentHash {
		t.Fatalf("unexpected events for delete path: %+v", events)
	}
}

func TestReconcileLeasesForNode_SkipsLeaseWhenNodeStillRoutable(t *testing.T) {
	pool := newRouterTestPool()
	plat := platform.NewPlatform("plat-reconcile-skip", "Plat-Reconcile-Skip", nil, nil)
	plat.StickyTTLNs = int64(time.Hour)
	pool.addPlatform(plat)

	currentHash, currentEntry := newRoutableEntry(t, `{"id":"skip-current"}`, "203.0.113.80")
	pool.addEntry(currentHash, currentEntry)
	pool.rebuildPlatformView(plat)

	var events []LeaseEvent
	router := newTestRouter(pool, func(e LeaseEvent) {
		events = append(events, e)
	})
	state, _ := router.states.LoadOrCompute(plat.ID, func() (*PlatformRoutingState, bool) {
		return NewPlatformRoutingState(), false
	})

	oldExpiry := time.Now().Add(time.Hour).UnixNano()
	oldLease := Lease{
		NodeHash:       currentHash,
		EgressIP:       currentEntry.GetEgressIP(),
		ExpiryNs:       oldExpiry,
		LastAccessedNs: time.Now().UnixNano(),
	}
	state.Leases.CreateLease("acct-skip", oldLease)

	router.ReconcileLeasesForNode(currentHash)

	lease, ok := state.Leases.GetLease("acct-skip")
	if !ok {
		t.Fatal("lease should remain when node is still routable")
	}
	if lease != oldLease {
		t.Fatalf("lease changed unexpectedly: got %+v want %+v", lease, oldLease)
	}
	if len(events) != 0 {
		t.Fatalf("expected no events when reconciliation is skipped, got %+v", events)
	}
}
