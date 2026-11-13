package config

import (
	"strings"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/dns"
)

func forkSubnetRaw() *RawConfig {
	c := DefaultRawConfig()
	c.ProxyGroup = []map[string]any{{"name": "Corp", "type": "subnet", "default": "Child", "matches": []any{map[string]any{"ssid": "GOLDWIND", "proxy": "DIRECT"}}}, {"name": "Child", "type": "select", "proxies": []string{"REJECT"}}}
	return c
}

func TestForkSubnetConfiguration(t *testing.T) {
	for _, field := range []string{"proxies", "use", "include-all", "include-all-proxies", "include-all-providers", "filter", "exclude-filter", "exclude-type"} {
		t.Run(field, func(t *testing.T) {
			c := forkSubnetRaw()
			c.ProxyGroup[0][field] = []string{"DIRECT"}
			if _, _, err := parseProxies(c); err == nil {
				t.Fatal("accepted forbidden " + field)
			}
		})
	}
	c := forkSubnetRaw()
	proxies, _, err := parseProxies(c)
	if err != nil {
		t.Fatal(err)
	}
	if proxies["Corp"].Type().String() != "Subnet" {
		t.Fatal("wrong adapter")
	}
	if _, exists := c.ProxyGroup[0]["proxies"]; exists {
		t.Fatal("parser mutated caller's raw config")
	}
	c = forkSubnetRaw()
	c.ProxyGroup[1]["proxies"] = []string{"Corp"}
	if _, _, err := parseProxies(c); err == nil {
		t.Fatal("cycle not rejected")
	}
}

func TestForkSubnetLoadChecks(t *testing.T) {
	raw := forkSubnetRaw()
	proxies, _, err := parseProxies(raw)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Proxies: proxies, DNS: &DNS{Enable: false, NameServer: []dns.NameServer{{Addr: "10.1.1.1:53", ProxyName: "Typo"}}}}
	if _, err := forkSubnetChecks(cfg, raw, true); err == nil {
		t.Fatal("hook target accepted")
	}
	if _, err := forkSubnetChecks(cfg, raw, false); err != nil {
		t.Fatal(err)
	}
	cfg.DNS.NameServer[0].ProxyName = "Corp"
	warnings, err := forkSubnetChecks(cfg, raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "dns.enable is false") {
		t.Fatal(warnings)
	}
	cfg.Proxies = nil
	if _, err := forkSubnetChecks(cfg, raw, true); err != nil {
		t.Fatal("unrelated config rejected", err)
	}
}

func TestForkSubnetWarnings(t *testing.T) {
	proxies := map[string]C.Proxy{"ts": adapter.NewProxy(outbound.NewBase(outbound.BaseOption{Name: "ts"}))}
	group, err := outboundgroup.ParseProxyGroup(map[string]any{"name": "Corp", "type": "subnet", "default": "ts", "matches": []any{map[string]any{"type": "wifi", "proxy": "ts"}}}, proxies, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxies["Corp"] = adapter.NewProxy(group)
	cfg := &Config{Proxies: proxies, DNS: &DNS{Enable: true, NameServerPolicy: []dns.Policy{{Domain: "+.example.com", NameServers: []dns.NameServer{{Addr: "10.1.1.1:53", ProxyName: "Corp"}}}}}}
	raw := &RawConfig{Proxy: []map[string]any{{"name": "ts", "type": "tailscale", "control-url": "https://headscale.example.com"}}}
	warnings, err := forkSubnetChecks(cfg, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(warnings, "\n")
	if !strings.Contains(text, "without UDP") || !strings.Contains(text, "possible DNS recursion") {
		t.Fatal(text)
	}
	cfg.DNS.ProxyServerNameserver = []dns.NameServer{{Addr: "1.1.1.1:53"}}
	warnings, _ = forkSubnetChecks(cfg, raw, false)
	if strings.Contains(strings.Join(warnings, "\n"), "recursion") {
		t.Fatal("dedicated proxy resolver warned")
	}
}

func TestForkSubnetRequiredMembers(t *testing.T) {
	for _, change := range []func(map[string]any){func(g map[string]any) { delete(g, "default") }, func(g map[string]any) { delete(g, "matches") }, func(g map[string]any) { g["matches"] = []any{} }, func(g map[string]any) { g["default"] = "Child*" }, func(g map[string]any) { g["matches"] = []any{map[string]any{"type": "wifi", "proxy": "Missing"}} }} {
		c := forkSubnetRaw()
		change(c.ProxyGroup[0])
		if _, _, err := parseProxies(c); err == nil {
			t.Fatalf("accepted invalid group %v", c.ProxyGroup[0])
		}
	}
}
