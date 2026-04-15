package proxy

import (
	"errors"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/Resinat/Resin/internal/routing"
	"github.com/sagernet/sing-box/adapter"
)

type routedOutbound struct {
	Route         routing.RouteResult
	Outbound      adapter.Outbound
	TransportKey  outboundTransportKey
	PassiveHealth bool
	ChainNodeHash node.Hash
}

func resolveRoutedOutbound(
	router *routing.Router,
	pool outbound.PoolAccessor,
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

	chain, err := outbound.ResolveForwardNodeChain(
		pool,
		router,
		resolveSubscriptionChainLookup(pool),
		result.NodeHash,
		target,
	)
	if err != nil {
		if errors.Is(err, outbound.ErrOutboundNotReady) ||
			errors.Is(err, outbound.ErrChainUnavailable) ||
			errors.Is(err, outbound.ErrChainTargetConflict) {
			return routedOutbound{}, ErrNoAvailableNodes
		}
		return routedOutbound{}, ErrInternalError
	}

	if !chain.MultiHop() {
		return routedOutbound{
			Route:         result,
			Outbound:      *obPtr,
			TransportKey:  directTransportKey(result.NodeHash),
			PassiveHealth: true,
		}, nil
	}

	if chainPool == nil {
		return routedOutbound{}, ErrInternalError
	}
	chainedOutbound, err := chainPool.Get(
		chain.Hops,
		chain.RawOptions,
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
		TransportKey:  chainTransportKey(chain.Hops...),
		PassiveHealth: false,
		ChainNodeHash: chain.ChainNodeHash,
	}, nil
}

func resolveSubscriptionChainLookup(pool outbound.PoolAccessor) outbound.SubscriptionChainLookup {
	if pool == nil {
		return nil
	}
	lookup, _ := pool.(outbound.SubscriptionChainLookup)
	return lookup
}
