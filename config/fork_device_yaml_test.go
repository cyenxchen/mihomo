package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const deviceOverrideWithEmptyPatch = "device-overrides: [{when: {device-name: other}, set: {}}]\n"

func TestDeviceOverridesYAMLEmpty(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"null", "device-overrides: null\n"},
		{"blank", "device-overrides:\n"},
		{"empty sequence", "device-overrides: []\n"},
		{"empty sequence alias", "x-empty: &empty []\ndevice-overrides: *empty\n"},
		{"inherited null", "x-base: &base {device-overrides: null}\n<<: *base\n"},
		{"explicit empty takes precedence", "x-base: &base {" + strings.TrimSpace(deviceOverrideWithEmptyPatch) + "}\n<<: *base\ndevice-overrides: []\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := []byte("allow-lan: true\nx-unused: {test: first, test: second}\n" + tc.source)
			cfg, err := UnmarshalRawConfig(source)
			if err != nil {
				t.Errorf("unmarshal no-op profile: %v", err)
			} else if !cfg.AllowLan || cfg.hasDeviceOverrides {
				t.Errorf("allow-lan=%t hasDeviceOverrides=%t, want base profile without overrides", cfg.AllowLan, cfg.hasDeviceOverrides)
			}
			expanded, err := ExpandDeviceOverrides(source, "linux", "gateway")
			if err != nil {
				t.Errorf("expand no-op profile: %v", err)
			} else if !bytes.Equal(expanded, source) {
				t.Errorf("expanding a no-op profile changed its YAML: %s", expanded)
			}
		})
	}
}

func TestDeviceOverridesYAMLRejectsMalformedNull(t *testing.T) {
	source := []byte("device-overrides: !!null invalid\n")
	if _, err := UnmarshalRawConfig(source); err == nil {
		t.Error("unmarshal accepted a malformed null override list")
	}
	if _, err := ExpandDeviceOverrides(source, "linux", "gateway"); err == nil {
		t.Error("expand accepted a malformed null override list")
	}
}

func TestDeviceOverridesYAMLMerge(t *testing.T) {
	t.Setenv("MIHOMO_DEVICE_NAME", "gateway")
	const source = `
x-main: &main
  enable: true
  ipv6: true
  nameserver: [1.1.1.1]
x-fallback: &fallback
  enable: false
  ipv6: false
  listen: '127.0.0.1:7874'
dns:
  enable: false
  <<: [*main, *fallback]
  nameserver-policy:
    '+.z.test': 1.1.1.1
    '+.a.test': 2.2.2.2
device-overrides:
  - when: {device-name: gateway}
    set:
      dns:
        nameserver-policy:
          '+.z.test': 3.3.3.3
          '+.m.test': 4.4.4.4
`
	cfg, err := UnmarshalRawConfig([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DNS.Enable || !cfg.DNS.IPv6 || cfg.DNS.Listen != "127.0.0.1:7874" || !slices.Equal(cfg.DNS.NameServer, []string{"1.1.1.1"}) {
		t.Fatalf("unexpected merged DNS fields: enable=%t ipv6=%t listen=%q nameserver=%v", cfg.DNS.Enable, cfg.DNS.IPv6, cfg.DNS.Listen, cfg.DNS.NameServer)
	}
	var domains, servers []string
	for pair := cfg.DNS.NameServerPolicy.Oldest(); pair != nil; pair = pair.Next() {
		domains = append(domains, pair.Key)
		server, _ := pair.Value.(string)
		servers = append(servers, server)
	}
	if !slices.Equal(domains, []string{"+.z.test", "+.a.test", "+.m.test"}) || !slices.Equal(servers, []string{"3.3.3.3", "2.2.2.2", "4.4.4.4"}) {
		t.Fatalf("unexpected DNS policy order or values: domains=%v servers=%v", domains, servers)
	}
}

func TestDeviceOverridesYAMLAliasIsolation(t *testing.T) {
	t.Setenv("MIHOMO_DEVICE_NAME", "gateway")
	const source = `
proxy-providers:
  primary: &provider {type: file, path: primary.yaml}
  backup: *provider
device-overrides:
  - when: {device-name: gateway}
    set: {proxy-providers: {primary: {path: override.yaml}}}
`
	cfg, err := UnmarshalRawConfig([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"primary": "override.yaml", "backup": "primary.yaml"} {
		provider := cfg.ProxyProvider[name]
		if provider["type"] != "file" || provider["path"] != want {
			t.Errorf("proxy-providers[%q] = %v, want file provider with path %q", name, provider, want)
		}
	}
}

func TestDeviceOverridesYAMLExpansion(t *testing.T) {
	const source = `
device-overrides:
  - when: {device-name: other}
    prepend: {rules: &base ['MATCH,DIRECT']}
rules: *base
`
	expanded, err := ExpandDeviceOverrides([]byte(source), "linux", "gateway")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(expanded), "device-overrides") {
		t.Fatalf("expanded profile contains device-overrides: %s", expanded)
	}
	cfg, err := UnmarshalRawConfig(expanded)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Rule, []string{"MATCH,DIRECT"}) {
		t.Fatalf("rules = %v, want [MATCH,DIRECT]", cfg.Rule)
	}
}

func TestDeviceOverridesYAMLValidation(t *testing.T) {
	for _, tc := range []struct{ name, source, diagnostic string }{
		{"duplicate key", "dns: {nameserver-policy: {test: 1.1.1.1, test: 2.2.2.2}}", "test"},
		{"invalid merge", "x-extra: {<<: [42]}", "merge"},
		{"alias cycle", "x-extra: &loop [*loop]", "alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UnmarshalRawConfig([]byte(tc.source + "\n" + deviceOverrideWithEmptyPatch))
			if err == nil || !strings.Contains(err.Error(), tc.diagnostic) {
				t.Fatalf("got error %v, want diagnostic containing %q", err, tc.diagnostic)
			}
		})
	}
}

func TestDeviceOverridesYAMLAliasExpansionLimit(t *testing.T) {
	var source strings.Builder
	source.WriteString(deviceOverrideWithEmptyPatch + "x-0: &a0 [word]\n")
	for i := 1; i <= 9; i++ {
		ref := fmt.Sprintf("*a%d", i-1)
		fmt.Fprintf(&source, "x-%d: &a%d [%s%s]\n", i, i, strings.Repeat(ref+", ", 4), ref)
	}
	_, err := ExpandDeviceOverrides([]byte(source.String()), "linux", "gateway")
	if err == nil || !strings.Contains(err.Error(), "alias expansion") {
		t.Fatalf("got error %v, want alias expansion diagnostic", err)
	}
}

func TestDeviceOverridesYAMLCompatibility(t *testing.T) {
	t.Run("extension mapping", func(t *testing.T) {
		const source = `
x-tuple: &tuple [linux, arm64]
x-client:
  ? *tuple
  : route-a
  ? [linux, amd64]
  : route-b
` + deviceOverrideWithEmptyPatch
		expanded, err := ExpandDeviceOverrides([]byte(source), "linux", "gateway")
		if err != nil {
			t.Fatal(err)
		}
		var reloaded struct {
			Client yaml.Node `yaml:"x-client"`
		}
		if err = yaml.Unmarshal(expanded, &reloaded); err != nil {
			t.Fatal(err)
		}
		pairs := reloaded.Client.Content
		if len(pairs) != 4 {
			t.Fatalf("expected two extension entries, got %s", expanded)
		}
		for i, arch := range []string{"arm64", "amd64"} {
			var key []string
			if err = pairs[2*i].Decode(&key); err != nil || !slices.Equal(key, []string{"linux", arch}) {
				t.Fatalf("key %d = %v, error = %v", i, key, err)
			}
			if value := pairs[2*i+1].Value; value != []string{"route-a", "route-b"}[i] {
				t.Fatalf("value %d = %q", i, value)
			}
		}
	})
	t.Run("binary string keys", func(t *testing.T) {
		t.Setenv("MIHOMO_DEVICE_NAME", "gateway")
		const source = `
!!binary cnVsZXM=: ['MATCH,REJECT']
dns: { !!binary bGlzdGVu: '127.0.0.1:7874' }
device-overrides:
  - when: {device-name: gateway}
    set: {dns: {listen: '0.0.0.0:7874'}}
    prepend: {rules: ['DOMAIN,a.test,DIRECT']}
`
		cfg, err := UnmarshalRawConfig([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(cfg.Rule, []string{"DOMAIN,a.test,DIRECT", "MATCH,REJECT"}) || cfg.DNS.Listen != "0.0.0.0:7874" {
			t.Fatalf("rules = %v, DNS listen = %q", cfg.Rule, cfg.DNS.Listen)
		}
	})
}

func TestDeviceOverridesYAMLAliasTagExpansionLimit(t *testing.T) {
	tag := "tag:" + strings.Repeat("x", 64<<10)
	source := deviceOverrideWithEmptyPatch + "x-tag: &tag !<" + tag + "> word\nx-refs: [" + strings.Repeat("*tag, ", 1024) + "*tag]\n"
	_, err := UnmarshalRawConfig([]byte(source))
	if err == nil || !strings.Contains(err.Error(), "alias expansion") {
		t.Fatalf("got error %v, want alias expansion diagnostic", err)
	}
}
