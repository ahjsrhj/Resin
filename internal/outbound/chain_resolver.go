package outbound

import (
	"encoding/json"
	"errors"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/platform"
)

var (
	ErrChainUnavailable    = errors.New("chain outbound unavailable")
	ErrChainTargetConflict = errors.New("chain target duplicates upstream hop")
)

type PlatformEntryLookup interface {
	GetPlatform(id string) (*platform.Platform, bool)
}

type SubscriptionChainLookup interface {
	ResolveNodeChainNodeHash(hash node.Hash) (node.Hash, bool)
}

type ResolvedNodeChain struct {
	Hops          []node.Hash
	RawOptions    []json.RawMessage
	TargetHash    node.Hash
	EntryNodeHash node.Hash
	ChainNodeHash node.Hash
}

func (c ResolvedNodeChain) MultiHop() bool {
	return len(c.Hops) > 1
}

func ResolveForwardNodeChain(
	pool PoolAccessor,
	platformLookup PlatformEntryLookup,
	subscriptionLookup SubscriptionChainLookup,
	platformID string,
	targetHash node.Hash,
) (ResolvedNodeChain, error) {
	prefix := make([]node.Hash, 0, 2)
	entryHash := resolvePlatformEntryNodeHash(platformLookup, platformID)
	if entryHash != node.Zero {
		prefix = append(prefix, entryHash)
	}

	chainHash := resolveSubscriptionChainNodeHash(subscriptionLookup, targetHash)
	if chainHash != node.Zero {
		prefix = append(prefix, chainHash)
	}

	return resolveNodeChain(pool, prefix, targetHash, entryHash, chainHash)
}

func ResolveProbeNodeChain(
	pool PoolAccessor,
	subscriptionLookup SubscriptionChainLookup,
	targetHash node.Hash,
) (ResolvedNodeChain, error) {
	chainHash := resolveSubscriptionChainNodeHash(subscriptionLookup, targetHash)
	prefix := make([]node.Hash, 0, 1)
	if chainHash != node.Zero {
		prefix = append(prefix, chainHash)
	}
	return resolveNodeChain(pool, prefix, targetHash, node.Zero, chainHash)
}

func resolveSubscriptionChainNodeHash(subscriptionLookup SubscriptionChainLookup, targetHash node.Hash) node.Hash {
	if subscriptionLookup == nil || targetHash == node.Zero {
		return node.Zero
	}
	chainHash, ok := subscriptionLookup.ResolveNodeChainNodeHash(targetHash)
	if !ok {
		return node.Zero
	}
	return chainHash
}

func resolvePlatformEntryNodeHash(platformLookup PlatformEntryLookup, platformID string) node.Hash {
	if platformLookup == nil || platformID == "" {
		return node.Zero
	}
	plat, ok := platformLookup.GetPlatform(platformID)
	if !ok || plat == nil {
		return node.Zero
	}
	return plat.EntryNodeHash
}

func resolveNodeChain(
	pool PoolAccessor,
	prefix []node.Hash,
	targetHash node.Hash,
	entryHash node.Hash,
	chainHash node.Hash,
) (ResolvedNodeChain, error) {
	if pool == nil || targetHash == node.Zero {
		return ResolvedNodeChain{}, ErrChainUnavailable
	}

	seenPrefix := make(map[node.Hash]struct{}, len(prefix))
	hops := make([]node.Hash, 0, len(prefix)+1)
	raws := make([]json.RawMessage, 0, len(prefix)+1)

	for _, hash := range prefix {
		if hash == node.Zero {
			continue
		}
		if _, seen := seenPrefix[hash]; seen {
			continue
		}
		entry, ok := pool.GetEntry(hash)
		if !ok || entry == nil || entry.Outbound.Load() == nil {
			return ResolvedNodeChain{}, ErrChainUnavailable
		}
		seenPrefix[hash] = struct{}{}
		hops = append(hops, hash)
		raws = append(raws, CloneRawOptions(entry.RawOptions))
	}

	if _, conflicted := seenPrefix[targetHash]; conflicted {
		return ResolvedNodeChain{}, ErrChainTargetConflict
	}

	targetEntry, ok := pool.GetEntry(targetHash)
	if !ok || targetEntry == nil || targetEntry.Outbound.Load() == nil {
		return ResolvedNodeChain{}, ErrChainUnavailable
	}

	hops = append(hops, targetHash)
	raws = append(raws, CloneRawOptions(targetEntry.RawOptions))

	return ResolvedNodeChain{
		Hops:          hops,
		RawOptions:    raws,
		TargetHash:    targetHash,
		EntryNodeHash: entryHash,
		ChainNodeHash: chainHash,
	}, nil
}
