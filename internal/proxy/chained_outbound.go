package proxy

import "github.com/Resinat/Resin/internal/outbound"

type ChainedOutboundPool = outbound.ChainedOutboundPool

func NewChainedOutboundPool() *ChainedOutboundPool {
	return outbound.NewChainedOutboundPool()
}
