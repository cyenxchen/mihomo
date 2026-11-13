package tunnel

import C "github.com/metacubex/mihomo/constant"

// Test-only entry avoids importing outbound -> dns -> tunnel from this package.
func ForkTestMatchWithConfig(p map[string]C.Proxy, r []C.Rule, m *C.Metadata) (C.Proxy, C.Rule, error) {
	beforeProxies, beforeRules := proxies, rules
	defer func() { proxies, rules = beforeProxies, beforeRules }()
	proxies, rules = p, r
	return match(m, C.RuleMatchHelper{})
}
