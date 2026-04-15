package topology

import (
	"strings"

	"github.com/Resinat/Resin/internal/node"
	"github.com/Resinat/Resin/internal/subscription"
)

func (p *GlobalNodePool) ResolveNodeChainPlatformID(hash node.Hash) (string, bool) {
	if p == nil || p.subLookup == nil {
		return "", false
	}

	entry, ok := p.GetEntry(hash)
	if !ok || entry == nil {
		return "", false
	}
	subIDs := entry.SubscriptionIDs()
	if len(subIDs) == 0 {
		return "", false
	}

	if sub, ok := p.pickNodeChainSubscription(hash, subIDs, true); ok {
		return resolveSubscriptionChainPlatformID(sub)
	}
	sub, ok := p.pickNodeChainSubscription(hash, subIDs, false)
	if !ok {
		return "", false
	}
	return resolveSubscriptionChainPlatformID(sub)
}

func (p *GlobalNodePool) pickNodeChainSubscription(hash node.Hash, subIDs []string, enabledOnly bool) (*subscription.Subscription, bool) {
	var (
		bestFound       bool
		bestCreatedAtNs int64
		bestSubID       string
		bestSub         *subscription.Subscription
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

		createdAtNs := sub.CreatedAtNs
		if !bestFound ||
			createdAtNs < bestCreatedAtNs ||
			(createdAtNs == bestCreatedAtNs && subID < bestSubID) {
			bestFound = true
			bestCreatedAtNs = createdAtNs
			bestSubID = subID
			bestSub = sub
		}
	}

	if !bestFound {
		return nil, false
	}
	return bestSub, true
}

func resolveSubscriptionChainPlatformID(sub interface{ ChainPlatformID() string }) (string, bool) {
	if sub == nil {
		return "", false
	}
	platformID := strings.TrimSpace(sub.ChainPlatformID())
	if platformID == "" {
		return "", false
	}
	return platformID, true
}
