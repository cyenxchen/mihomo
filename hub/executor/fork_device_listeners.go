package executor

import (
	"errors"
	"fmt"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/listener"
	"github.com/metacubex/mihomo/tunnel"
)

// ForkCheckDeviceListeners retries a bind that conflicted with a retiring
// listener during the first pass (for example, DNS moving to the old proxy port).
func ForkCheckDeviceListeners(cfg *config.Config) error {
	var errs []error
	if cfg.DNS.Enable && cfg.DNS.Listen != "" {
		lc := inbound.NewListenConfig()
		lc.SetRouteMark(cfg.DNS.ListenRoutingMark)
		if err := dns.EnsureServer(cfg.DNS.Listen, lc, resolver.DefaultService); err != nil {
			errs = append(errs, err)
		}
	}
	ports := listener.GetPorts()
	g := cfg.General
	if ports.Port != g.Port {
		listener.ReCreateHTTP(g.Port, tunnel.Tunnel)
	}
	if ports.SocksPort != g.SocksPort {
		listener.ReCreateSocks(g.SocksPort, tunnel.Tunnel)
	}
	if ports.MixedPort != g.MixedPort {
		listener.ReCreateMixed(g.MixedPort, tunnel.Tunnel)
	}
	if err := listener.EnsureRedir(g.RedirPort, tunnel.Tunnel); err != nil {
		errs = append(errs, fmt.Errorf("redir proxy failed to bind port %d: %w", g.RedirPort, err))
	}
	if err := listener.EnsureTProxy(g.TProxyPort, tunnel.Tunnel); err != nil {
		errs = append(errs, fmt.Errorf("tproxy failed to bind port %d: %w", g.TProxyPort, err))
	}
	if err := listener.EnsureInboundListeners(cfg.Listeners, tunnel.Tunnel, true, false); err != nil {
		errs = append(errs, err)
	}
	ports = listener.GetPorts()
	if ports.Port != g.Port {
		errs = append(errs, fmt.Errorf("HTTP proxy failed to bind port %d", g.Port))
	}
	if ports.SocksPort != g.SocksPort {
		errs = append(errs, fmt.Errorf("SOCKS proxy failed to bind port %d", g.SocksPort))
	}
	if ports.MixedPort != g.MixedPort {
		errs = append(errs, fmt.Errorf("mixed proxy failed to bind port %d", g.MixedPort))
	}
	return errors.Join(errs...)
}
