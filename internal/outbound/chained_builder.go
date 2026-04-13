package outbound

import (
	"encoding/json"
	"fmt"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	sJson "github.com/sagernet/sing/common/json"
)

type ChainedOutboundBundle struct {
	Outbound  adapter.Outbound
	outbounds []adapter.Outbound
	builder   *SingboxBuilder
}

func NewChainedOutboundBundle(
	rawOptions []json.RawMessage,
	tags []string,
) (*ChainedOutboundBundle, error) {
	if len(rawOptions) == 0 || len(rawOptions) != len(tags) {
		return nil, fmt.Errorf("new chained outbound bundle: invalid hop inputs")
	}

	builder, err := NewSingboxBuilder()
	if err != nil {
		return nil, err
	}

	outbounds := make([]adapter.Outbound, 0, len(rawOptions))
	prevTag := ""
	for i, raw := range rawOptions {
		ob, buildErr := builder.buildManagedOutbound(raw, tags[i], prevTag)
		if buildErr != nil {
			for j := len(outbounds) - 1; j >= 0; j-- {
				closeOutbound(outbounds[j])
			}
			_ = builder.Close()
			return nil, buildErr
		}
		outbounds = append(outbounds, ob)
		prevTag = tags[i]
	}

	return &ChainedOutboundBundle{
		Outbound:  outbounds[len(outbounds)-1],
		outbounds: outbounds,
		builder:   builder,
	}, nil
}

func (b *ChainedOutboundBundle) Close() error {
	if b == nil {
		return nil
	}
	for i := len(b.outbounds) - 1; i >= 0; i-- {
		closeOutbound(b.outbounds[i])
	}
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

func MakeChainOutboundTags(tagSeeds ...string) []string {
	tags := make([]string, 0, len(tagSeeds))
	for i, seed := range tagSeeds {
		tags = append(tags, fmt.Sprintf("resin-chain-hop-%d-%s", i+1, seed))
	}
	return tags
}

func CloneRawOptions(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
