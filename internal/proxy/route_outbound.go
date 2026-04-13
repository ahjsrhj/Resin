package proxy

import (
	"errors"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routedOutbound struct {
	Route          routing.RouteResult
	Outbound       adapter.Outbound
	TransportKey   outboundTransportKey
	PassiveHealth  bool
	EntryNodeHash  node.Hash
}

func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
	platformLookup PlatformLookup,
	chainPool *ChainedOutboundPool,
	platformName string,
	account string,
	target string,
) (routedOutbound, *ProxyError) {
	result, err := router.RouteRequest(platformName, account, target)
	if err != nil {
		return routedOutbound{}, mapRouteError(err)
	}

	targetEntry, ok := pool.GetEntry(result.NodeHash)
	if !ok {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	obPtr := targetEntry.Outbound.Load()
	if obPtr == nil {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	entryNodeHash := resolvePlatformEntryNodeHash(platformLookup, result.PlatformID)
	if entryNodeHash == node.Zero {
		return routedOutbound{
			Route:         result,
			Outbound:      *obPtr,
			TransportKey:  directTransportKey(result.NodeHash),
			PassiveHealth: true,
		}, nil
	}

	if entryNodeHash == result.NodeHash {
		return routedOutbound{}, ErrNoAvailableNodes
	}

	entryNode, ok := pool.GetEntry(entryNodeHash)
	if !ok || entryNode == nil || !entryNode.HasOutbound() {
		return routedOutbound{}, ErrNoAvailableNodes
	}
	if chainPool == nil {
		return routedOutbound{}, ErrInternalError
	}
	chainedOutbound, err := chainPool.Get(
		entryNodeHash,
		result.NodeHash,
		entryNode.RawOptions,
		targetEntry.RawOptions,
	)
	if err != nil {
		if errors.Is(err, outbound.ErrOutboundNotReady) {
			return routedOutbound{}, ErrNoAvailableNodes
		}
		return routedOutbound{}, ErrInternalError
	}

	return routedOutbound{
		Route:         result,
		Outbound:      chainedOutbound,
		TransportKey:  chainTransportKey(entryNodeHash, result.NodeHash),
		PassiveHealth: false,
		EntryNodeHash: entryNodeHash,
	}, nil
}

func resolvePlatformEntryNodeHash(platformLookup PlatformLookup, platformID string) node.Hash {
	if platformLookup == nil || platformID == "" {
		return node.Zero
	}
	plat, ok := platformLookup.GetPlatform(platformID)
	if !ok || plat == nil {
		return node.Zero
	}
	return plat.EntryNodeHash
}
