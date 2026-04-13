package outbound

import (
	"encoding/json"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	sJson "github.com/sagernet/sing/common/json"
)

type ChainedOutboundBundle struct {
	Outbound adapter.Outbound
	entry    adapter.Outbound
	target   adapter.Outbound
	builder  *SingboxBuilder
}

func NewChainedOutboundBundle(
	entryRaw json.RawMessage,
	targetRaw json.RawMessage,
	entryTag string,
	targetTag string,
) (*ChainedOutboundBundle, error) {
	builder, err := NewSingboxBuilder()
	if err != nil {
		return nil, err
	}

	entryOutbound, err := builder.buildManagedOutbound(entryRaw, entryTag, "")
	if err != nil {
		_ = builder.Close()
		return nil, err
	}
	targetOutbound, err := builder.buildManagedOutbound(targetRaw, targetTag, entryTag)
	if err != nil {
		closeOutbound(entryOutbound)
		_ = builder.Close()
		return nil, err
	}

	return &ChainedOutboundBundle{
		Outbound: targetOutbound,
		entry:    entryOutbound,
		target:   targetOutbound,
		builder:  builder,
	}, nil
}

func (b *ChainedOutboundBundle) Close() error {
	if b == nil {
		return nil
	}
	closeOutbound(b.target)
	closeOutbound(b.entry)
	if b.builder != nil {
		return b.builder.Close()
	}
	return nil
}

func (b *SingboxBuilder) buildManagedOutbound(
	rawOptions json.RawMessage,
	tag string,
	detour string,
) (adapter.Outbound, error) {
	if b == nil || b.outboundManager == nil {
		return nil, fmt.Errorf("build chained outbound: missing outbound manager")
	}

	outboundConfig, err := b.parseOutboundConfig(rawOptions)
	if err != nil {
		return nil, err
	}
	outboundConfig.Tag = tag
	if detour != "" {
		dialerOptions, ok := outboundConfig.Options.(option.DialerOptionsWrapper)
		if !ok {
			return nil, fmt.Errorf("outbound type %s does not support detour", outboundConfig.Type)
		}
		dialer := dialerOptions.TakeDialerOptions()
		dialer.Detour = detour
		dialerOptions.ReplaceDialerOptions(dialer)
	}

	logger := b.logFactory.NewLogger("outbound/" + outboundConfig.Type + "/chain")
	if err := b.outboundManager.Create(
		b.ctx,
		nil,
		logger,
		outboundConfig.Tag,
		outboundConfig.Type,
		outboundConfig.Options,
	); err != nil {
		return nil, fmt.Errorf("create chained outbound [%s]: %w", outboundConfig.Type, err)
	}

	ob, ok := b.outboundManager.Outbound(outboundConfig.Tag)
	if !ok {
		return nil, fmt.Errorf("create chained outbound [%s]: not registered", outboundConfig.Type)
	}
	for _, stage := range adapter.ListStartStages {
		if err := adapter.LegacyStart(ob, stage); err != nil {
			closeOutbound(ob)
			return nil, fmt.Errorf("outbound start %s [%s]: %w", stage, outboundConfig.Type, err)
		}
	}

	return ob, nil
}

func (b *SingboxBuilder) parseOutboundConfig(rawOptions json.RawMessage) (option.Outbound, error) {
	var outboundConfig option.Outbound
	if err := sJson.UnmarshalContext(b.ctx, rawOptions, &outboundConfig); err != nil {
		return option.Outbound{}, fmt.Errorf("parse outbound options: %w", err)
	}
	return outboundConfig, nil
}

func MakeChainOutboundTags(entryTagSeed, targetTagSeed string) (string, string) {
	return "resin-chain-entry-" + entryTagSeed, "resin-chain-target-" + targetTagSeed
}

func CloneRawOptions(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
