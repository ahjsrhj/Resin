package outbound

import (
	"encoding/json"
	"errors"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/routing"
)

var (
	ErrChainUnavailable    = errors.New("chain outbound unavailable")
	ErrChainTargetConflict = errors.New("chain target duplicates upstream hop")
)

type ChainPlatformRouter interface {
	RouteRequestByID(platformID, account, target string) (routing.RouteResult, error)
}

type SubscriptionChainLookup interface {
	ResolveNodeChainPlatformID(hash node.Hash) (string, bool)
}

type ResolvedNodeChain struct {
	Hops          []node.Hash
	RawOptions    []json.RawMessage
	TargetHash    node.Hash
	ChainNodeHash node.Hash
}

func (c ResolvedNodeChain) MultiHop() bool {
	return len(c.Hops) > 1
}

func ResolveForwardNodeChain(
	pool PoolAccessor,
	router ChainPlatformRouter,
	subscriptionLookup SubscriptionChainLookup,
	targetHash node.Hash,
	target string,
) (ResolvedNodeChain, error) {
	chainHash := resolveSubscriptionChainHopHash(router, subscriptionLookup, targetHash, target)
	prefix := make([]node.Hash, 0, 1)
	if chainHash != node.Zero {
		prefix = append(prefix, chainHash)
	}
	return resolveNodeChain(pool, prefix, targetHash, chainHash)
}

func ResolveProbeNodeChain(
	pool PoolAccessor,
	router ChainPlatformRouter,
	subscriptionLookup SubscriptionChainLookup,
	targetHash node.Hash,
	target string,
) (ResolvedNodeChain, error) {
	chainHash := resolveSubscriptionChainHopHash(router, subscriptionLookup, targetHash, target)
	prefix := make([]node.Hash, 0, 1)
	if chainHash != node.Zero {
		prefix = append(prefix, chainHash)
	}
	return resolveNodeChain(pool, prefix, targetHash, chainHash)
}

func resolveSubscriptionChainHopHash(
	router ChainPlatformRouter,
	subscriptionLookup SubscriptionChainLookup,
	targetHash node.Hash,
	target string,
) node.Hash {
	if router == nil || subscriptionLookup == nil || targetHash == node.Zero || target == "" {
		return node.Zero
	}
	chainPlatformID, ok := subscriptionLookup.ResolveNodeChainPlatformID(targetHash)
	if !ok || chainPlatformID == "" {
		return node.Zero
	}
	result, err := router.RouteRequestByID(chainPlatformID, "", target)
	if err != nil {
		return node.Zero
	}
	return result.NodeHash
}

func resolveNodeChain(
	pool PoolAccessor,
	prefix []node.Hash,
	targetHash node.Hash,
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
		entry, ok := pool.GetEntry(hash)
		if !ok || entry == nil || entry.Outbound.Load() == nil {
			continue
		}
		if _, seen := seenPrefix[hash]; seen {
			continue
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

	actualChainHash := node.Zero
	if _, ok := seenPrefix[chainHash]; ok {
		actualChainHash = chainHash
	}

	return ResolvedNodeChain{
		Hops:          hops,
		RawOptions:    raws,
		TargetHash:    targetHash,
		ChainNodeHash: actualChainHash,
	}, nil
}
