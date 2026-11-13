package config_test

import (
	"strings"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/common/yaml"
	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/config"
	_ "github.com/metacubex/mihomo/hub/executor" // supplies temporaryUpdateGeneral
)

func TestForkSubnetDNSUnknownHookTarget(t *testing.T) {
	before := dialer.DefaultSocketHook
	defer func() { dialer.DefaultSocketHook = before }()
	dialer.DefaultSocketHook = func(string, string, syscall.RawConn) error { return nil }
	c := config.DefaultRawConfig()
	c.ProxyGroup = []map[string]any{{"name": "Corp", "type": "subnet", "default": "REJECT", "matches": []any{map[string]any{"type": "wifi", "proxy": "DIRECT"}}}}
	c.DNS.NameServer = []string{"1.1.1.1#Typo"}
	c.DNS.FallbackFilter.GeoIP = false
	_, err := config.ParseRawConfig(c)
	if err == nil || !strings.Contains(err.Error(), "Typo") {
		t.Fatalf("unknown DNS target accepted or wrong error: %v", err)
	}
}

func TestReviewU1YAMLTimeoutValidation(t *testing.T) {
	for _, tc := range []struct {
		value string
		valid bool
	}{{"1", true}, {"10", true}, {"0", false}, {"-1", false}, {"1.5", false}, {"10.0", false}, {`"10"`, false}, {"null", false}, {"true", false}} {
		t.Run(tc.value, func(t *testing.T) {
			raw := config.DefaultRawConfig()
			raw.DNS.FallbackFilter.GeoIP = false
			source := "proxy-groups:\n  - name: Corp\n    type: subnet\n    default: REJECT\n    unknown-timeout: " + tc.value + "\n    matches:\n      - {type: wifi, proxy: DIRECT}\n"
			if err := yaml.Unmarshal([]byte(source), raw); err != nil {
				t.Fatal(err)
			}
			parsed, err := config.ParseRawConfig(raw)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Proxies["Corp"] == nil {
					t.Fatal("subnet missing")
				}
			} else if err == nil || !strings.Contains(err.Error(), "unknown-timeout") {
				t.Fatalf("invalid timeout accepted or wrong error: %v", err)
			}
		})
	}
}
