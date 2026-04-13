package topology

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
)

func newChainLookupTestPool(t *testing.T) (*SubscriptionManager, *GlobalNodePool) {
	t.Helper()
	subMgr := NewSubscriptionManager()
	pool := NewGlobalNodePool(PoolConfig{
		SubLookup:              subMgr.Lookup,
		GeoLookup:              func(netip.Addr) string { return "" },
		MaxLatencyTableEntries: 16,
		MaxConsecutiveFailures: func() int { return 3 },
		LatencyDecayWindow:     func() time.Duration { return time.Minute },
	})
	return subMgr, pool
}

func TestGlobalNodePool_ResolveNodeChainNodeHash_PrefersEnabledEarliestSubscription(t *testing.T) {
	subMgr, pool := newChainLookupTestPool(t)
	targetHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))
	chainA := node.HashFromRawOptions([]byte(`{"type":"socks","server":"2.2.2.2"}`))
	chainB := node.HashFromRawOptions([]byte(`{"type":"socks","server":"3.3.3.3"}`))

	subA := subscription.NewSubscription("sub-a", "A", "https://example.com/a", false, false)
	subA.CreatedAtNs = 10
	subA.SetChainNodeHash(chainA.Hex())
	subA.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"a"}})
	subMgr.Register(subA)

	subB := subscription.NewSubscription("sub-b", "B", "https://example.com/b", true, false)
	subB.CreatedAtNs = 20
	subB.SetChainNodeHash(chainB.Hex())
	subB.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"b"}})
	subMgr.Register(subB)

	entry := node.NewNodeEntry(targetHash, []byte(`{"type":"ss","server":"1.1.1.1"}`), time.Now(), 16)
	entry.AddSubscriptionID(subA.ID)
	entry.AddSubscriptionID(subB.ID)
	pool.LoadNodeFromBootstrap(entry)

	got, ok := pool.ResolveNodeChainNodeHash(targetHash)
	if !ok {
		t.Fatal("expected chain node hash to resolve")
	}
	if got != chainB {
		t.Fatalf("resolved chain node hash = %s, want %s", got.Hex(), chainB.Hex())
	}
}

func TestGlobalNodePool_ResolveNodeChainNodeHash_FallsBackToEarliestWhenNoEnabledHolder(t *testing.T) {
	subMgr, pool := newChainLookupTestPool(t)
	targetHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"4.4.4.4"}`))
	chainA := node.HashFromRawOptions([]byte(`{"type":"socks","server":"5.5.5.5"}`))
	chainB := node.HashFromRawOptions([]byte(`{"type":"socks","server":"6.6.6.6"}`))

	subA := subscription.NewSubscription("sub-a", "A", "https://example.com/a", false, false)
	subA.CreatedAtNs = 10
	subA.SetChainNodeHash(chainA.Hex())
	subA.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"a"}})
	subMgr.Register(subA)

	subB := subscription.NewSubscription("sub-b", "B", "https://example.com/b", false, false)
	subB.CreatedAtNs = 20
	subB.SetChainNodeHash(chainB.Hex())
	subB.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"b"}})
	subMgr.Register(subB)

	entry := node.NewNodeEntry(targetHash, []byte(`{"type":"ss","server":"4.4.4.4"}`), time.Now(), 16)
	entry.AddSubscriptionID(subA.ID)
	entry.AddSubscriptionID(subB.ID)
	pool.LoadNodeFromBootstrap(entry)

	got, ok := pool.ResolveNodeChainNodeHash(targetHash)
	if !ok {
		t.Fatal("expected chain node hash to resolve")
	}
	if got != chainA {
		t.Fatalf("resolved chain node hash = %s, want %s", got.Hex(), chainA.Hex())
	}
}
