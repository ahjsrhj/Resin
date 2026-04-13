package outbound

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
	"github.com/Resinat/Resin/internal/testutil"
)

type chainResolverPool struct {
	entries map[node.Hash]*node.NodeEntry
	plat    *platform.Platform
	chain   map[node.Hash]node.Hash
}

func (p *chainResolverPool) GetEntry(hash node.Hash) (*node.NodeEntry, bool) {
	entry, ok := p.entries[hash]
	return entry, ok
}

func (p *chainResolverPool) RangeNodes(fn func(node.Hash, *node.NodeEntry) bool) {}

func (p *chainResolverPool) GetPlatform(id string) (*platform.Platform, bool) {
	if p.plat == nil || p.plat.ID != id {
		return nil, false
	}
	return p.plat, true
}

func (p *chainResolverPool) ResolveNodeChainNodeHash(hash node.Hash) (node.Hash, bool) {
	if p.chain == nil {
		return node.Zero, false
	}
	chainHash, ok := p.chain[hash]
	return chainHash, ok
}

func newResolverNode(raw []byte, egress string) (node.Hash, *node.NodeEntry) {
	hash := node.HashFromRawOptions(raw)
	entry := node.NewNodeEntry(hash, raw, time.Now(), 16)
	entry.SetEgressIP(netip.MustParseAddr(egress))
	ob := testutil.NewNoopOutbound()
	entry.Outbound.Store(&ob)
	return hash, entry
}

func TestResolveForwardNodeChain_BuildsThreeHopChain(t *testing.T) {
	entryHash, entryNode := newResolverNode([]byte(`{"type":"socks","server":"1.1.1.1","server_port":1080}`), "1.1.1.1")
	chainHash, chainNode := newResolverNode([]byte(`{"type":"socks","server":"2.2.2.2","server_port":1080}`), "2.2.2.2")
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			entryHash:  entryNode,
			chainHash:  chainNode,
			targetHash: targetNode,
		},
		plat: &platform.Platform{ID: "plat-1", EntryNodeHash: entryHash},
		chain: map[node.Hash]node.Hash{
			targetHash: chainHash,
		},
	}

	chain, err := ResolveForwardNodeChain(pool, pool, pool, "plat-1", targetHash)
	if err != nil {
		t.Fatalf("ResolveForwardNodeChain: %v", err)
	}
	if len(chain.Hops) != 3 {
		t.Fatalf("hop count = %d, want 3", len(chain.Hops))
	}
	if chain.Hops[0] != entryHash || chain.Hops[1] != chainHash || chain.Hops[2] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
}

func TestResolveForwardNodeChain_DeduplicatesRepeatedPrefixHop(t *testing.T) {
	entryHash, entryNode := newResolverNode([]byte(`{"type":"socks","server":"1.1.1.1","server_port":1080}`), "1.1.1.1")
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			entryHash:  entryNode,
			targetHash: targetNode,
		},
		plat: &platform.Platform{ID: "plat-1", EntryNodeHash: entryHash},
		chain: map[node.Hash]node.Hash{
			targetHash: entryHash,
		},
	}

	chain, err := ResolveForwardNodeChain(pool, pool, pool, "plat-1", targetHash)
	if err != nil {
		t.Fatalf("ResolveForwardNodeChain: %v", err)
	}
	if len(chain.Hops) != 2 {
		t.Fatalf("hop count = %d, want 2", len(chain.Hops))
	}
	if chain.Hops[0] != entryHash || chain.Hops[1] != targetHash {
		t.Fatalf("resolved hops = %v", chain.Hops)
	}
}

func TestResolveProbeNodeChain_RejectsTargetConflict(t *testing.T) {
	targetHash, targetNode := newResolverNode([]byte(`{"type":"socks","server":"3.3.3.3","server_port":1080}`), "3.3.3.3")

	pool := &chainResolverPool{
		entries: map[node.Hash]*node.NodeEntry{
			targetHash: targetNode,
		},
		chain: map[node.Hash]node.Hash{
			targetHash: targetHash,
		},
	}

	_, err := ResolveProbeNodeChain(pool, pool, targetHash)
	if err == nil {
		t.Fatal("expected ResolveProbeNodeChain to reject target conflict")
	}
	if err != ErrChainTargetConflict {
		t.Fatalf("err = %v, want %v", err, ErrChainTargetConflict)
	}
}
