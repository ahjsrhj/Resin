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
	entries               map[node.Hash]*node.NodeEntry
	platformsByID         map[string]*platform.Platform
	platformsByName       map[string]*platform.Platform
	chainPlatformByTarget map[node.Hash]string
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

func (p *chainedRoutePool) ResolveNodeChainPlatformID(hash node.Hash) (string, bool) {
	if p.chainPlatformByTarget == nil {
		return "", false
	}
	platformID, ok := p.chainPlatformByTarget[hash]
	return platformID, ok
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

	targetHost, targetPort := closedTCPAddr(t)
	chainHost, chainPort := closedTCPAddr(t)
	targetRaw := socksOutboundRaw(targetHost, targetPort)
	chainRaw := socksOutboundRaw(chainHost, chainPort)

	targetHash := node.HashFromRawOptions(targetRaw)
	chainHash := node.HashFromRawOptions(chainRaw)

	makeNode := func(hash node.Hash, raw []byte, egress string, subID string, withLatency bool) *node.NodeEntry {
		entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
		entry.AddSubscriptionID(subID)
		entry.SetEgressIP(netip.MustParseAddr(egress))
		if withLatency {
			entry.LatencyTable.LoadEntry("example.com", node.DomainLatencyStats{
				Ewma:        25 * time.Millisecond,
				LastUpdated: time.Now(),
			})
		}
		ob := testutil.NewNoopOutbound()
		entry.Outbound.Store(&ob)
		return entry
	}

	pool := &chainedRoutePool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: makeNode(targetHash, targetRaw, "2.2.2.2", "sub-target", true),
			chainHash:  makeNode(chainHash, chainRaw, "3.3.3.3", "sub-chain", true),
		},
		platformsByID:         make(map[string]*platform.Platform),
		platformsByName:       make(map[string]*platform.Platform),
		chainPlatformByTarget: make(map[node.Hash]string),
	}

	subLookup := func(subID string, hash node.Hash) (string, bool, []string, bool) {
		switch subID {
		case "sub-target":
			return "TargetSub", true, []string{"target"}, true
		case "sub-chain":
			return "ChainSub", true, []string{"chain"}, true
		default:
			return "", false, nil, false
		}
	}

	targetRegex, err := platform.CompileRegexFilters([]string{`^TargetSub/.*`})
	if err != nil {
		t.Fatalf("compile target regex: %v", err)
	}
	chainRegex, err := platform.CompileRegexFilters([]string{`^ChainSub/.*`})
	if err != nil {
		t.Fatalf("compile chain regex: %v", err)
	}

	targetPlat := platform.NewConfiguredPlatform(
		"plat-1",
		"plat",
		targetRegex,
		nil,
		int64(time.Hour),
		string(platform.ReverseProxyMissActionTreatAsEmpty),
		string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		"",
		string(platform.AllocationPolicyBalanced),
	)
	targetPlat.FullRebuild(pool.RangeNodes, subLookup, nil)
	pool.platformsByID[targetPlat.ID] = targetPlat
	pool.platformsByName[targetPlat.Name] = targetPlat

	chainPlat := platform.NewConfiguredPlatform(
		"plat-chain",
		"chain",
		chainRegex,
		nil,
		int64(time.Hour),
		string(platform.ReverseProxyMissActionTreatAsEmpty),
		string(platform.ReverseProxyEmptyAccountBehaviorRandom),
		"",
		string(platform.AllocationPolicyBalanced),
	)
	chainPlat.FullRebuild(pool.RangeNodes, subLookup, nil)
	pool.platformsByID[chainPlat.ID] = chainPlat
	pool.platformsByName[chainPlat.Name] = chainPlat
	pool.chainPlatformByTarget[targetHash] = chainPlat.ID

	router := routing.NewRouter(routing.RouterConfig{
		Pool:        pool,
		Authorities: func() []string { return []string{"example.com"} },
		P2CWindow:   func() time.Duration { return time.Minute },
	})

	return router, pool, chainHash, targetHash
}

func TestResolveRoutedOutbound_WithSubscriptionChainBuildsChainedOutboundAndCaches(t *testing.T) {
	router, pool, chainHash, targetHash := buildChainedRouteEnv(t)
	chainPool := NewChainedOutboundPool()
	t.Cleanup(chainPool.CloseAll)

	first, perr := resolveRoutedOutbound(router, pool, chainPool, "plat", "", "example.com:443")
	if perr != nil {
		t.Fatalf("resolveRoutedOutbound first: %v", perr)
	}
	second, perr := resolveRoutedOutbound(router, pool, chainPool, "plat", "", "example.com:443")
	if perr != nil {
		t.Fatalf("resolveRoutedOutbound second: %v", perr)
	}

	if first.PassiveHealth {
		t.Fatal("expected chained route to disable passive health")
	}
	if first.Route.NodeHash != targetHash {
		t.Fatalf("target node hash = %s, want %s", first.Route.NodeHash.Hex(), targetHash.Hex())
	}
	if first.ChainNodeHash != chainHash {
		t.Fatalf("chain node hash = %s, want %s", first.ChainNodeHash.Hex(), chainHash.Hex())
	}
	if first.TransportKey != chainTransportKey(chainHash, targetHash) {
		t.Fatalf("transport key = %+v, want %+v", first.TransportKey, chainTransportKey(chainHash, targetHash))
	}
	if first.Outbound != second.Outbound {
		t.Fatal("expected chained outbound bundle to be reused for the same chain/target pair")
	}
}

func TestResolveRoutedOutbound_DeduplicatesSubscriptionChainHop(t *testing.T) {
	router, pool, _, targetHash := buildChainedRouteEnv(t)
	pool.chainPlatformByTarget[targetHash] = "plat-1"
	chainPool := NewChainedOutboundPool()
	t.Cleanup(chainPool.CloseAll)

	_, perr := resolveRoutedOutbound(router, pool, chainPool, "plat", "", "example.com:443")
	if perr == nil {
		t.Fatal("expected resolveRoutedOutbound to reject target/chain hop conflict")
	}
	if perr != ErrNoAvailableNodes {
		t.Fatalf("error = %v, want %v", perr, ErrNoAvailableNodes)
	}
}

func TestResolveRoutedOutbound_MissingSubscriptionChainFallsBackToDirectRoute(t *testing.T) {
	router, pool, _, targetHash := buildChainedRouteEnv(t)
	pool.chainPlatformByTarget[targetHash] = "missing-plat"
	chainPool := NewChainedOutboundPool()
	t.Cleanup(chainPool.CloseAll)

	routed, perr := resolveRoutedOutbound(router, pool, chainPool, "plat", "", "example.com:443")
	if perr != nil {
		t.Fatalf("resolveRoutedOutbound: %v", perr)
	}
	if !routed.PassiveHealth {
		t.Fatal("expected direct route to keep passive health enabled")
	}
	if routed.ChainNodeHash != node.Zero {
		t.Fatalf("chain node hash = %s, want zero", routed.ChainNodeHash.Hex())
	}
	if routed.Route.NodeHash != targetHash {
		t.Fatalf("target node hash = %s, want %s", routed.Route.NodeHash.Hex(), targetHash.Hex())
	}
	if routed.TransportKey != directTransportKey(targetHash) {
		t.Fatalf("transport key = %+v, want %+v", routed.TransportKey, directTransportKey(targetHash))
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
