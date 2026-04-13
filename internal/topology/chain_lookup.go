package topology

import "github.com/Resinat/Resin/internal/node"

func (p *GlobalNodePool) ResolveNodeChainNodeHash(hash node.Hash) (node.Hash, bool) {
	if p == nil || p.subLookup == nil {
		return node.Zero, false
	}

	entry, ok := p.GetEntry(hash)
	if !ok || entry == nil {
		return node.Zero, false
	}
	subIDs := entry.SubscriptionIDs()
	if len(subIDs) == 0 {
		return node.Zero, false
	}

	if chainHash, ok := p.pickNodeChainNodeHash(hash, subIDs, true); ok {
		return chainHash, true
	}
	return p.pickNodeChainNodeHash(hash, subIDs, false)
}

func (p *GlobalNodePool) pickNodeChainNodeHash(hash node.Hash, subIDs []string, enabledOnly bool) (node.Hash, bool) {
	var (
		bestFound       bool
		bestCreatedAtNs int64
		bestSubID       string
		bestChainHash   node.Hash
	)

	for _, subID := range subIDs {
		sub := p.subLookup(subID)
		if sub == nil {
			continue
		}
		managed, ok := sub.ManagedNodes().LoadNode(hash)
		if !ok || managed.Evicted {
			continue
		}
		if enabledOnly {
			if !sub.Enabled() || managed.Disabled {
				continue
			}
		}

		chainHash := resolveSubscriptionChainHash(sub)
		createdAtNs := sub.CreatedAtNs
		if !bestFound ||
			createdAtNs < bestCreatedAtNs ||
			(createdAtNs == bestCreatedAtNs && subID < bestSubID) {
			bestFound = true
			bestCreatedAtNs = createdAtNs
			bestSubID = subID
			bestChainHash = chainHash
		}
	}

	if !bestFound {
		return node.Zero, false
	}
	return bestChainHash, true
}

func resolveSubscriptionChainHash(sub interface{ ChainNodeHash() string }) node.Hash {
	if sub == nil {
		return node.Zero
	}
	return parseSubscriptionChainHash(sub.ChainNodeHash())
}

func parseSubscriptionChainHash(raw string) node.Hash {
	if raw == "" {
		return node.Zero
	}
	hash, err := node.ParseHex(raw)
	if err != nil {
		return node.Zero
	}
	return hash
}
