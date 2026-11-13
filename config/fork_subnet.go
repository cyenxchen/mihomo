package config

import (
	"fmt"
	"maps"
	"net/url"
	"runtime"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/component/trie"
	"github.com/metacubex/mihomo/dns"
	"github.com/metacubex/mihomo/log"
)

func forkPrepareSubnetGroups(groups []map[string]any) ([]map[string]any, error) {
	out := make([]map[string]any, len(groups))
	for i, g := range groups {
		out[i] = maps.Clone(g)
		if g["type"] != "subnet" {
			continue
		}
		members, err := outboundgroup.ForkSubnetMembers(g, true)
		if err != nil {
			return nil, fmt.Errorf("subnet %v: %w", g["name"], err)
		}
		out[i]["proxies"] = members
	}
	return out, nil
}

func forkCheckSubnetConfig(cfg *Config, raw *RawConfig) error {
	warnings, err := forkSubnetChecks(cfg, raw, runtime.GOOS == "android" || dialer.DefaultSocketHook != nil)
	for _, w := range warnings {
		log.Warnln("[Subnet] %s", w)
	}
	return err
}

func forkSubnetChecks(cfg *Config, raw *RawConfig, hook bool) ([]string, error) {
	groups := map[string]*outboundgroup.Subnet{}
	for name, p := range cfg.Proxies {
		if g, ok := p.Adapter().(*outboundgroup.Subnet); ok {
			groups[name] = g
		}
	}
	if len(groups) == 0 {
		return nil, nil
	}
	var warnings []string
	d := cfg.DNS
	if !d.Enable {
		warnings = append(warnings, "dns.enable is false; subnet domain resolution uses system DNS")
	}
	servers := append([]dns.NameServer{}, d.NameServer...)
	for _, list := range [][]dns.NameServer{d.Fallback, d.DefaultNameserver, d.ProxyServerNameserver, d.DirectNameServer} {
		servers = append(servers, list...)
	}
	for _, list := range [][]dns.Policy{d.NameServerPolicy, d.ProxyServerPolicy} {
		for _, p := range list {
			servers = append(servers, p.NameServers...)
		}
	}
	for _, ns := range servers {
		if hook && ns.ProxyName != "" && ns.ProxyName != "RULES" && cfg.Proxies[ns.ProxyName] == nil {
			return warnings, fmt.Errorf("subnet: DNS proxy %q not found with socket hook", ns.ProxyName)
		}
		if g := groups[ns.ProxyName]; g != nil && ns.Net == "" {
			for _, p := range g.Proxies() {
				if !p.SupportUDP() {
					warnings = append(warnings, fmt.Sprintf("DNS server %s via %s has member %s without UDP; set udp: true or use TCP DNS", ns.Addr, g.Name(), p.Name()))
				}
			}
		}
	}
	if len(d.ProxyServerNameserver) == 0 {
		for _, p := range raw.Proxy {
			if p["type"] != "tailscale" {
				continue
			}
			control, _ := p["control-url"].(string)
			u, err := url.Parse(control)
			if err != nil || u.Hostname() == "" {
				continue
			}
			for _, policy := range d.NameServerPolicy {
				matched := false
				if policy.Matcher != nil {
					matched = policy.Matcher.MatchDomain(u.Hostname())
				} else {
					tree := trie.New[bool]()
					if tree.Insert(policy.Domain, true) == nil {
						matched = tree.Search(u.Hostname()) != nil
					}
				}
				if !matched {
					continue
				}
				for _, ns := range policy.NameServers {
					if g := groups[ns.ProxyName]; g != nil {
						for _, member := range g.Proxies() {
							if member.Name() == p["name"] {
								warnings = append(warnings, fmt.Sprintf("Tailscale control-url %s resolves through subnet %s; possible DNS recursion", u.Hostname(), g.Name()))
							}
						}
					}
				}
			}
		}
	}
	return warnings, nil
}
