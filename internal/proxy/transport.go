package proxy

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/Resinat/Resin/internal/node"
	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

type OutboundTransportConfig struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	IdleConnTimeout     time.Duration
}

type outboundTransportKey struct {
	EntryHash  node.Hash
	TargetHash node.Hash
}

func directTransportKey(targetHash node.Hash) outboundTransportKey {
	return outboundTransportKey{TargetHash: targetHash}
}

func chainTransportKey(entryHash, targetHash node.Hash) outboundTransportKey {
	return outboundTransportKey{EntryHash: entryHash, TargetHash: targetHash}
}

const (
	defaultTransportMaxIdleConns        = 1024
	defaultTransportMaxIdleConnsPerHost = 64
	defaultTransportIdleConnTimeout     = 90 * time.Second
)

func normalizeOutboundTransportConfig(cfg OutboundTransportConfig) OutboundTransportConfig {
	if cfg.MaxIdleConns <= 0 {
		cfg.MaxIdleConns = defaultTransportMaxIdleConns
	}
	if cfg.MaxIdleConnsPerHost <= 0 {
		cfg.MaxIdleConnsPerHost = defaultTransportMaxIdleConnsPerHost
	}
	if cfg.IdleConnTimeout <= 0 {
		cfg.IdleConnTimeout = defaultTransportIdleConnTimeout
	}
	return cfg
}

// OutboundTransportPool manages reusable outbound HTTP transports keyed by node hash.
// A single instance should be shared by forward/reverse proxies so keep-alive pools
// are reused and can be evicted on node removal.
type OutboundTransportPool struct {
	config     OutboundTransportConfig
	transports *xsync.Map[outboundTransportKey, *http.Transport]
}

func newOutboundTransportPool() *OutboundTransportPool {
	return NewOutboundTransportPool(OutboundTransportConfig{})
}

func newOutboundTransportPoolWithConfig(cfg OutboundTransportConfig) *OutboundTransportPool {
	return NewOutboundTransportPool(cfg)
}

// NewOutboundTransportPool creates a transport pool with normalized settings.
func NewOutboundTransportPool(cfg OutboundTransportConfig) *OutboundTransportPool {
	return &OutboundTransportPool{
		config:     normalizeOutboundTransportConfig(cfg),
		transports: xsync.NewMap[outboundTransportKey, *http.Transport](),
	}
}

// Get returns a reusable transport for the given node hash.
func (p *OutboundTransportPool) Get(
	key outboundTransportKey,
	ob adapter.Outbound,
	sink MetricsEventSink,
) *http.Transport {
	transport, _ := p.transports.LoadOrCompute(key, func() (*http.Transport, bool) {
		return p.newReusableOutboundTransport(ob, sink), false
	})
	return transport
}

// EvictNode closes idle connections for transports associated with the node.
func (p *OutboundTransportPool) EvictNode(hash node.Hash) {
	if p == nil {
		return
	}
	var doomed []*http.Transport
	p.transports.Range(func(key outboundTransportKey, transport *http.Transport) bool {
		if key.EntryHash != hash && key.TargetHash != hash {
			return true
		}
		if removed, ok := p.transports.LoadAndDelete(key); ok && removed != nil {
			doomed = append(doomed, removed)
		}
		return true
	})
	for _, transport := range doomed {
		transport.CloseIdleConnections()
	}
}

// CloseAll closes idle connections and clears all pooled transports.
func (p *OutboundTransportPool) CloseAll() {
	p.transports.Range(func(_ outboundTransportKey, transport *http.Transport) bool {
		if transport != nil {
			transport.CloseIdleConnections()
		}
		return true
	})
	p.transports.Clear()
}

func (p *OutboundTransportPool) newReusableOutboundTransport(ob adapter.Outbound, sink MetricsEventSink) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := ob.DialContext(ctx, network, M.ParseSocksaddr(addr))
			if err != nil {
				return nil, err
			}
			if sink != nil {
				sink.OnConnectionLifecycle(ConnectionOutbound, ConnectionOpen)
				conn = newCountingConn(conn, sink)
			}
			return conn, nil
		},
		DisableKeepAlives:   false,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        p.config.MaxIdleConns,
		MaxIdleConnsPerHost: p.config.MaxIdleConnsPerHost,
		IdleConnTimeout:     p.config.IdleConnTimeout,
	}
}
