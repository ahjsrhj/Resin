package proxy

import (
	"fmt"
	"sync"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/outbound"
	"github.com/sagernet/sing-box/adapter"
)

type ChainedOutboundPool struct {
	mu      sync.Mutex
	bundles map[outboundTransportKey]*outbound.ChainedOutboundBundle
}

func NewChainedOutboundPool() *ChainedOutboundPool {
	return &ChainedOutboundPool{
		bundles: make(map[outboundTransportKey]*outbound.ChainedOutboundBundle),
	}
}

func (p *ChainedOutboundPool) Get(
	entryHash node.Hash,
	targetHash node.Hash,
	entryRaw []byte,
	targetRaw []byte,
) (adapter.Outbound, error) {
	if p == nil {
		return nil, fmt.Errorf("chained outbound pool is nil")
	}

	key := chainTransportKey(entryHash, targetHash)

	p.mu.Lock()
	if bundle := p.bundles[key]; bundle != nil {
		p.mu.Unlock()
		return bundle.Outbound, nil
	}
	p.mu.Unlock()

	entryTag, targetTag := outbound.MakeChainOutboundTags(entryHash.Hex(), targetHash.Hex())
	bundle, err := outbound.NewChainedOutboundBundle(
		outbound.CloneRawOptions(entryRaw),
		outbound.CloneRawOptions(targetRaw),
		entryTag,
		targetTag,
	)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if existing := p.bundles[key]; existing != nil {
		p.mu.Unlock()
		_ = bundle.Close()
		return existing.Outbound, nil
	}
	p.bundles[key] = bundle
	p.mu.Unlock()

	return bundle.Outbound, nil
}

func (p *ChainedOutboundPool) EvictNode(hash node.Hash) {
	if p == nil {
		return
	}

	var doomed []*outbound.ChainedOutboundBundle

	p.mu.Lock()
	for key, bundle := range p.bundles {
		if key.EntryHash != hash && key.TargetHash != hash {
			continue
		}
		delete(p.bundles, key)
		doomed = append(doomed, bundle)
	}
	p.mu.Unlock()

	for _, bundle := range doomed {
		_ = bundle.Close()
	}
}

func (p *ChainedOutboundPool) CloseAll() {
	if p == nil {
		return
	}

	p.mu.Lock()
	bundles := p.bundles
	p.bundles = make(map[outboundTransportKey]*outbound.ChainedOutboundBundle)
	p.mu.Unlock()

	for _, bundle := range bundles {
		_ = bundle.Close()
	}
}
