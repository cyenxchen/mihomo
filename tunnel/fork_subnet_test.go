package tunnel_test

import (
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	C "github.com/metacubex/mihomo/constant"
	RC "github.com/metacubex/mihomo/rules/common"
	"github.com/metacubex/mihomo/tunnel"
)

func TestForkSubnetUDPRuleCannotFallThrough(t *testing.T) {
	proxies := map[string]C.Proxy{"NoUDP": adapter.NewProxy(outbound.NewBase(outbound.BaseOption{Name: "NoUDP"})), "DIRECT": adapter.NewProxy(outbound.NewDirect())}
	g, err := outboundgroup.ParseProxyGroup(map[string]any{"name": "Corp", "type": "subnet", "default": "NoUDP", "matches": []any{map[string]any{"type": "wifi", "proxy": "NoUDP"}}}, proxies, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	proxies["Corp"] = adapter.NewProxy(g)
	rules := []C.Rule{RC.NewMatch("Corp"), RC.NewMatch("DIRECT")}
	got, rule, err := tunnel.ForkTestMatchWithConfig(proxies, rules, &C.Metadata{NetWork: C.UDP})
	if err != nil || got.Name() != "Corp" || rule != rules[0] {
		t.Fatalf("UDP subnet rule skipped: %v %v %v", got, rule, err)
	}
}
