package outbound

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
)

type ChainKey struct {
	HopCount uint8
	Hops     [3]node.Hash
}

func MakeChainKey(hops []node.Hash) ChainKey {
	var key ChainKey
	if len(hops) > len(key.Hops) {
		hops = hops[:len(key.Hops)]
	}
	key.HopCount = uint8(len(hops))
	copy(key.Hops[:], hops)
	return key
}

type ChainedOutboundPool struct {
	mu      sync.Mutex
	bundles map[ChainKey]*ChainedOutboundBundle
}

func NewChainedOutboundPool() *ChainedOutboundPool {
	return &ChainedOutboundPool{
		bundles: make(map[ChainKey]*ChainedOutboundBundle),
	}
}

func (p *ChainedOutboundPool) Get(
	hops []node.Hash,
	rawOptions []json.RawMessage,
) (adapter.Outbound, error) {
	if p == nil {
		return nil, fmt.Errorf("chained outbound pool is nil")
	}
	if len(hops) != len(rawOptions) {
		return nil, fmt.Errorf("chained outbound pool: hops/raw mismatch")
	}
	if len(hops) < 2 {
		return nil, fmt.Errorf("chained outbound pool: requires at least 2 hops")
	}

	key := MakeChainKey(hops)

	p.mu.Lock()
	if bundle := p.bundles[key]; bundle != nil {
		p.mu.Unlock()
		return bundle.Outbound, nil
	}
	p.mu.Unlock()

	tagSeeds := make([]string, 0, len(hops))
	raws := make([]json.RawMessage, 0, len(rawOptions))
	for i, hash := range hops {
		tagSeeds = append(tagSeeds, hash.Hex())
		raws = append(raws, CloneRawOptions(rawOptions[i]))
	}
	tags := MakeChainOutboundTags(tagSeeds...)
	bundle, err := NewChainedOutboundBundle(raws, tags)
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

	var doomed []*ChainedOutboundBundle

	p.mu.Lock()
	for key, bundle := range p.bundles {
		if !chainKeyContainsHash(key, hash) {
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
	p.bundles = make(map[ChainKey]*ChainedOutboundBundle)
	p.mu.Unlock()

	for _, bundle := range bundles {
		_ = bundle.Close()
	}
}

func chainKeyContainsHash(key ChainKey, hash node.Hash) bool {
	for i := uint8(0); i < key.HopCount; i++ {
		if key.Hops[i] == hash {
			return true
		}
	}
	return false
}
