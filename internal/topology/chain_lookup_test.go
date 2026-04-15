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

func TestGlobalNodePool_ResolveNodeChainPlatformID_PrefersEnabledEarliestSubscription(t *testing.T) {
	subMgr, pool := newChainLookupTestPool(t)
	targetHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"1.1.1.1"}`))
	chainPlatformIDA := "11111111-1111-1111-1111-111111111111"
	chainPlatformIDB := "22222222-2222-2222-2222-222222222222"

	subA := subscription.NewSubscription("sub-a", "A", "https://example.com/a", false, false)
	subA.CreatedAtNs = 10
	subA.SetChainPlatformID(chainPlatformIDA)
	subA.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"a"}})
	subMgr.Register(subA)

	subB := subscription.NewSubscription("sub-b", "B", "https://example.com/b", true, false)
	subB.CreatedAtNs = 20
	subB.SetChainPlatformID(chainPlatformIDB)
	subB.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"b"}})
	subMgr.Register(subB)

	entry := node.NewNodeEntry(targetHash, []byte(`{"type":"ss","server":"1.1.1.1"}`), time.Now(), 16)
	entry.AddSubscriptionID(subA.ID)
	entry.AddSubscriptionID(subB.ID)
	pool.LoadNodeFromBootstrap(entry)

	got, ok := pool.ResolveNodeChainPlatformID(targetHash)
	if !ok {
		t.Fatal("expected chain platform id to resolve")
	}
	if got != chainPlatformIDB {
		t.Fatalf("resolved chain platform id = %q, want %q", got, chainPlatformIDB)
	}
}

func TestGlobalNodePool_ResolveNodeChainPlatformID_FallsBackToEarliestWhenNoEnabledHolder(t *testing.T) {
	subMgr, pool := newChainLookupTestPool(t)
	targetHash := node.HashFromRawOptions([]byte(`{"type":"ss","server":"4.4.4.4"}`))
	chainPlatformIDA := "33333333-3333-3333-3333-333333333333"
	chainPlatformIDB := "44444444-4444-4444-4444-444444444444"

	subA := subscription.NewSubscription("sub-a", "A", "https://example.com/a", false, false)
	subA.CreatedAtNs = 10
	subA.SetChainPlatformID(chainPlatformIDA)
	subA.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"a"}})
	subMgr.Register(subA)

	subB := subscription.NewSubscription("sub-b", "B", "https://example.com/b", false, false)
	subB.CreatedAtNs = 20
	subB.SetChainPlatformID(chainPlatformIDB)
	subB.ManagedNodes().StoreNode(targetHash, subscription.ManagedNode{Tags: []string{"b"}})
	subMgr.Register(subB)

	entry := node.NewNodeEntry(targetHash, []byte(`{"type":"ss","server":"4.4.4.4"}`), time.Now(), 16)
	entry.AddSubscriptionID(subA.ID)
	entry.AddSubscriptionID(subB.ID)
	pool.LoadNodeFromBootstrap(entry)

	got, ok := pool.ResolveNodeChainPlatformID(targetHash)
	if !ok {
		t.Fatal("expected chain platform id to resolve")
	}
	if got != chainPlatformIDA {
		t.Fatalf("resolved chain platform id = %q, want %q", got, chainPlatformIDA)
	}
}
