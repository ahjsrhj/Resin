package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/testutil"
)

type chainedRoutePool struct {
	entries         map[node.Hash]*node.NodeEntry
	platformsByID   map[string]*platform.Platform
	platformsByName map[string]*platform.Platform
}

func (p *chainedRoutePool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	entry, ok := p.entries[hash]
	return entry, ok
}

func (p *chainedRoutePool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {
	for hash, entry := range p.entries {
		if !fn(hash, entry) {
			return
		}
	}
}

func (p *chainedRoutePool) GetPlatform(id string) (*platform.Platform, bool) {
	plat, ok := p.platformsByID[id]
	return plat, ok
}

func (p *chainedRoutePool) GetPlatformByName(name string) (*platform.Platform, bool) {
	plat, ok := p.platformsByName[name]
	return plat, ok
}

func (p *chainedRoutePool) RangePlatforms(fn func(*platform.Platform) bool) {
	for _, plat := range p.platformsByID {
		if !fn(plat) {
			return
		}
	}
}

func closedTCPAddr(t *testing.T) (string, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen temp tcp: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close temp tcp listener: %v", err)
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("atoi port: %v", err)
	}
	return host, port
}

func socksOutboundRaw(host string, port int) []byte {
	return []byte(`{"type":"socks","server":"` + host + `","server_port":` + strconv.Itoa(port) + `,"version":"5"}`)
}

func buildChainedRouteEnv(t *testing.T) (*routing.Router, *chainedRoutePool, node.Hash, node.Hash) {
	t.Helper()

	entryHost, entryPort := closedTCPAddr(t)
	targetHost, targetPort := closedTCPAddr(t)
	entryRaw := socksOutboundRaw(entryHost, entryPort)
	targetRaw := socksOutboundRaw(targetHost, targetPort)

	entryHash := node.HashFromRawOptions(entryRaw)
	targetHash := node.HashFromRawOptions(targetRaw)

	makeNode := func(hash node.Hash, raw []byte, egress string) *node.NodeEntry {
		entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
		entry.AddSubscriptionID("sub-1")
		entry.SetEgressIP(netip.MustParseAddr(egress))
		entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
			Ewma:        25 * time.Millisecond,
			LastUpdated: time.Now(),
		})
		ob := testutil.NewNoopOutbound()
		entry.Outbound.Store(&ob)
		return entry
	}

	pool := &chainedRoutePool{
		entries: map[node.Hash]*node.NodeEntry{
			entryHash:  makeNode(entryHash, entryRaw, "1.1.1.1"),
			targetHash: makeNode(targetHash, targetRaw, "2.2.2.2"),
		},
		platformsByID:   make(map[string]*platform.Platform),
		platformsByName: make(map[string]*platform.Platform),
	}

	plat := platform.NewConfiguredPlatform(
		"plat-1",
		"plat",
		nil,
		nil,
		entryHash,
		int64(time.Hour),
		string(platform.ReverseProxyMissActionTreatAsEmpty),
		string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		"",
		string(platform.AllocationPolicyBalanced),
	)
	plat.FullRebuild(
		pool.RangeNodes,
		func(subID string, hash node.Hash) (string, bool, []string, bool) {
			return "sub", true, []string{"tag"}, true
		},
		nil,
	)
	pool.platformsByID[plat.ID] = plat
	pool.platformsByName[plat.Name] = plat

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return time.Minute },
	})

	return router, pool, entryHash, targetHash
}

func TestResolveRoutedOutbound_WithEntryNodeBuildsChainedOutboundAndCaches(t *testing.T) {
	router, pool, entryHash, targetHash := buildChainedRouteEnv(t)
	chainPool := NewChainedOutboundPool()
	t.Cleanup(chainPool.CloseAll)

	first, perr := resolveRoutedOutbound(router, pool, pool, chainPool, "plat", "", "example.com:443")
	if perr != nil {
		t.Fatalf("resolveRoutedOutbound first: %v", perr)
	}
	second, perr := resolveRoutedOutbound(router, pool, pool, chainPool, "plat", "", "example.com:443")
	if perr != nil {
		t.Fatalf("resolveRoutedOutbound second: %v", perr)
	}

	if first.PassiveHealth {
		t.Fatal("expected chained route to disable passive health")
	}
	if first.EntryNodeHash != entryHash {
		t.Fatalf("entry node hash = %s, want %s", first.EntryNodeHash.Hex(), entryHash.Hex())
	}
	if first.Route.NodeHash != targetHash {
		t.Fatalf("target node hash = %s, want %s", first.Route.NodeHash.Hex(), targetHash.Hex())
	}
	if first.TransportKey != chainTransportKey(entryHash, targetHash) {
		t.Fatalf("transport key = %+v, want %+v", first.TransportKey, chainTransportKey(entryHash, targetHash))
	}
	if first.Outbound != second.Outbound {
		t.Fatal("expected chained outbound bundle to be reused for the same entry/target pair")
	}
}

func TestForwardProxy_ChainedRouteSkipsPassiveHealthOnFailure(t *testing.T) {
	router, pool, _, _ := buildChainedRouteEnv(t)
	chainPool := NewChainedOutboundPool()
	t.Cleanup(chainPool.CloseAll)

	health := &mockHealthRecorder{}
	forward := NewForwardProxy(ForwardProxyConfig{
		ProxyToken:     "tok",
		Router:         router,
		Pool:           pool,
		PlatformLookup: pool,
		Health:         health,
		Events:         NoOpEventEmitter{},
		ChainPool:      chainPool,
	})

	req := httptest.NewRequest(http.MethodGet, "http://example.com/resource", nil)
	req.Header.Set("Proxy-Authorization", basicAuth("tok", "plat:acct"))
	rec := httptest.NewRecorder()

	forward.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if health.resultCalls.Load() != 0 {
		t.Fatalf("passive health result calls = %d, want 0", health.resultCalls.Load())
	}
	if health.latencyCalls.Load() != 0 {
		t.Fatalf("passive health latency calls = %d, want 0", health.latencyCalls.Load())
	}
}
