package outbound

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/Resinat/Resin/internal/testutil"
)

type chainResolverPool struct {
	entries  map[node.Hash]*node.NodeEntry
	chainIDs map[node.Hash]string
}

func (p *chainResolverPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	entry, ok := p.entries[hash]
	return entry, ok
}

func (p *chainResolverPool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {}

func (p *chainResolverPool) ResolveNodeChainPlatformID(hash node.Hash) (string, bool) {
	if p.chainIDs == nil {
		return "", false
	}
	platformID, ok := p.chainIDs[hash]
	return platformID, ok
}

type chainResolverRouter struct {
	routes map[string]node.Hash
	errs   map[string]error
}

func (r *chainResolverRouter) RouteRequestByID(platformID, account, target string) (routing.RouteResult, error) {
	if err := r.errs[platformID]; err != nil {
		return routing.RouteResult{}, err
	}
	hash, ok := r.routes[platformID]
	if !ok {
		return routing.RouteResult{}, errors.New("platform route not found")
	}
	return routing.RouteResult{PlatformID: platformID, NodeHash: hash}, nil
}

func newResolverNode(raw []byte, egress string) (node.Hash, *node.NodeEntry) {
	hash := node.HashFromRawOptions(raw)
	entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
	entry.SetEgressIP(netip.MustParseAddr(egress))
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	return hash, entry
}

func TestResolveForwardNodeChain_BuildsTwoHopChain(t *testing.T) {
	chainHash, chainNode := newResolverNode([]byte(`{"type":"socks","server":"2.2.2.2","server_port":1080}`), "2.2.2.2")
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			chainHash:  chainNode,
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		routes: map[string]node.Hash{
			"plat-chain": chainHash,
		},
	}

	chain, err := ResolveForwardNodeChain(pool, router, pool, targetHash, "example.com:443")
	if err != nil {
		t.Fatalf("ResolveForwardNodeChain: %v", err)
	}
	if len(chain.Hops) != 2 {
		t.Fatalf("hop count = %d, want 2", len(chain.Hops))
	}
	if chain.Hops[0] != chainHash || chain.Hops[1] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
	if chain.ChainNodeHash != chainHash {
		t.Fatalf("chain node hash = %s, want %s", chain.ChainNodeHash.Hex(), chainHash.Hex())
	}
}

func TestResolveForwardNodeChain_DeduplicatesRepeatedChainHop(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		routes: map[string]node.Hash{
			"plat-chain": targetHash,
		},
	}

	_, err := ResolveForwardNodeChain(pool, router, pool, targetHash, "example.com:443")
	if err != ErrChainTargetConflict {
		t.Fatalf("err = %v, want %v", err, ErrChainTargetConflict)
	}
}

func TestResolveProbeNodeChain_RejectsTargetConflict(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		routes: map[string]node.Hash{
			"plat-chain": targetHash,
		},
	}

	_, err := ResolveProbeNodeChain(pool, router, pool, targetHash, "https://example.com")
	if err != ErrChainTargetConflict {
		t.Fatalf("err = %v, want %v", err, ErrChainTargetConflict)
	}
}

func TestResolveForwardNodeChain_SkipsMissingSubscriptionChainHop(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		routes: map[string]node.Hash{
			"plat-chain": node.HashFromRawOptions([]byte(`{"type":"socks","server":"8.8.8.8","server_port":1080}`)),
		},
	}

	chain, err := ResolveForwardNodeChain(pool, router, pool, targetHash, "example.com:443")
	if err != nil {
		t.Fatalf("ResolveForwardNodeChain: %v", err)
	}
	if len(chain.Hops) != 1 {
		t.Fatalf("hop count = %d, want 1", len(chain.Hops))
	}
	if chain.Hops[0] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
	if chain.ChainNodeHash != node.Zero {
		t.Fatalf("chain node hash = %s, want zero", chain.ChainNodeHash.Hex())
	}
}

func TestResolveForwardNodeChain_DegradesWhenChainPlatformRoutingFails(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		errs: map[string]error{
			"plat-chain": routing.ErrPlatformNotFound,
		},
	}

	chain, err := ResolveForwardNodeChain(pool, router, pool, targetHash, "example.com:443")
	if err != nil {
		t.Fatalf("ResolveForwardNodeChain: %v", err)
	}
	if len(chain.Hops) != 1 || chain.Hops[0] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
}

func TestResolveProbeNodeChain_SkipsMissingSubscriptionChainHop(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chainIDs: map[node.Hash]string{
			targetHash: "plat-chain",
		},
	}
	router := &chainResolverRouter{
		routes: map[string]node.Hash{
			"plat-chain": node.HashFromRawOptions([]byte(`{"type":"socks","server":"7.7.7.7","server_port":1080}`)),
		},
	}

	chain, err := ResolveProbeNodeChain(pool, router, pool, targetHash, "https://example.com")
	if err != nil {
		t.Fatalf("ResolveProbeNodeChain: %v", err)
	}
	if len(chain.Hops) != 1 || chain.Hops[0] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
	if chain.ChainNodeHash != node.Zero {
		t.Fatalf("chain node hash = %s, want zero", chain.ChainNodeHash.Hex())
	}
}
