package config

import (
	"bytes"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/log"
)

func TestDeviceOverridesLoad(t *testing.T) {
	source := `
allow-lan: false
external-controller: 127.0.0.1:9090
dns:
  enable: true
  listen: 127.0.0.1:7874
  nameserver: [1.1.1.1]
tun: {enable: false}
rules: ['MATCH,REJECT']
device-overrides:
  - when: {os: [OS, unused], device-name: ["Lab Mac", gateway]}
    set:
      allow-lan: true
      external-controller: 0.0.0.0:9090
      dns: {listen: '0.0.0.0:7874', nameserver: [8.8.8.8]}
    prepend: {rules: ['DOMAIN,a.test,DIRECT']}
  - when: {device-name: gateway}
    set: {external-controller: '192.0.2.1:9090', rules: ['MATCH,DIRECT']}
    prepend: {rules: ['DOMAIN,b.test,DIRECT']}
  - when: {os: other, device-name: gateway}
    set: {tun: {enable: true}}
`
	source = strings.ReplaceAll(source, "[OS,", "["+runtime.GOOS+",")
	for _, tc := range []struct {
		name, api, dns string
		lan            bool
		rules, servers []string
	}{
		{"gateway", "192.0.2.1:9090", "0.0.0.0:7874", true, []string{"DOMAIN,a.test,DIRECT", "DOMAIN,b.test,DIRECT", "MATCH,DIRECT"}, []string{"8.8.8.8"}},
		{"Lab Mac", "0.0.0.0:9090", "0.0.0.0:7874", true, []string{"DOMAIN,a.test,DIRECT", "MATCH,REJECT"}, []string{"8.8.8.8"}},
		{"unknown", "127.0.0.1:9090", "127.0.0.1:7874", false, []string{"MATCH,REJECT"}, []string{"1.1.1.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MIHOMO_DEVICE_NAME", tc.name)
			cfg, err := UnmarshalRawConfig([]byte(source))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AllowLan != tc.lan || cfg.ExternalController != tc.api || cfg.DNS.Listen != tc.dns || !cfg.DNS.Enable || cfg.Tun.Enable || !slices.Equal(cfg.Rule, tc.rules) || !slices.Equal(cfg.DNS.NameServer, tc.servers) {
				t.Fatalf("lan=%t api=%s dns=%s servers=%v rules=%v tun=%t", cfg.AllowLan, cfg.ExternalController, cfg.DNS.Listen, cfg.DNS.NameServer, cfg.Rule, cfg.Tun.Enable)
			}
		})
	}
}

func TestDeviceOverridesInvalid(t *testing.T) {
	for _, tc := range []struct{ block, field string }{
		{"{when: {}, set: {allow-lan: true}}", "when"},
		{"{when: {device-nam: gateway}, set: {allow-lan: true}}", "device-nam"},
		{"{when: {device-name: []}, set: {allow-lan: true}}", "device-name"},
		{"{when: {device-name: [true]}, set: {allow-lan: true}}", "device-name"},
		{"{when: {device-name: never}, set: {dns: {listn: foo}}}", "listn"},
		{"{when: {os: linux}, prepend: {proxies: []}}", "proxies"},
		{"{when: {os: linux}, prepend: {rules: [true]}}", "rules"},
		{"{when: {os: linux}, set: {device-overrides: []}}", "device-overrides"},
		{"{when: {os: linux}, sett: {allow-lan: true}}", "sett"},
		{"{when: {device-name: ''}, set: {allow-lan: true}}", "device-name"},
	} {
		if _, err := UnmarshalRawConfig([]byte("device-overrides:\n  - " + tc.block)); err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Errorf("invalid override %s: got %v", tc.block, err)
		}
	}
}

func TestDeviceOverridesAndroidEmbedding(t *testing.T) {
	const source = `
allow-lan: false
dns: {listen: '127.0.0.1:7874'}
rules: ['MATCH,REJECT']
device-overrides:
  - when: {os: android, device-name: "Home gateway"}
    set: {allow-lan: true, dns: {listen: '0.0.0.0:7874'}}
    prepend: {rules: ['DOMAIN,home.test,DIRECT']}
`
	t.Setenv("MIHOMO_DEVICE_NAME", "Home gateway")
	t.Run("legacy client uses base and warns", func(t *testing.T) {
		events := log.Subscribe()
		defer log.UnSubscribe(events)
		legacy := DefaultRawConfig()
		if err := forkUnmarshalDeviceConfig([]byte(source), legacy, true); err != nil {
			t.Fatal(err)
		}
		if legacy.AllowLan || legacy.DNS.Listen != "127.0.0.1:7874" || !slices.Equal(legacy.Rule, []string{"MATCH,REJECT"}) {
			t.Fatal("legacy client did not use the base profile")
		}
		timeout := time.After(2 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.LogLevel == log.WARNING && strings.Contains(ev.Payload, "device-overrides") {
					return
				}
			case <-timeout:
				t.Fatal("missing warning for legacy client")
			}
		}

	})
	for _, name := range []string{"", "Home gateway"} {
		expanded, err := ExpandDeviceOverrides([]byte(source), "android", name)
		if err != nil {
			t.Fatal(err)
		}
		// A second consumer must see exactly what the embedder used to prepare its VPN.
		second, err := ExpandDeviceOverrides(expanded, "linux", "different")
		if err != nil || !bytes.Equal(expanded, second) {
			t.Fatalf("expansion not idempotent: %v", err)
		}
		raw := DefaultRawConfig()
		if err := forkUnmarshalDeviceConfig(expanded, raw, true); err != nil {
			t.Fatal(err)
		}
		wantLAN := name != ""
		wantDNS, wantRules := "127.0.0.1:7874", []string{"MATCH,REJECT"}
		if wantLAN {
			wantDNS, wantRules = "0.0.0.0:7874", []string{"DOMAIN,home.test,DIRECT", "MATCH,REJECT"}
		}
		if raw.AllowLan != wantLAN || raw.DNS.Listen != wantDNS || !slices.Equal(raw.Rule, wantRules) {
			t.Fatalf("after Android preparation for %q: lan=%t dns=%q rules=%v", name, raw.AllowLan, raw.DNS.Listen, raw.Rule)
		}
	}
}

func TestDeviceOverridesRejectNullCondition(t *testing.T) {
	for _, pair := range []string{"os: null, device-name: gateway", "os: linux, device-name: null"} {
		cfg := "device-overrides:\n  - when: {" + pair + "}\n    set: {allow-lan: true}\n"
		if _, err := ExpandDeviceOverrides([]byte(cfg), "linux", "gateway"); err == nil {
			t.Errorf("accepted null in %s", pair)
		}
	}
}

func TestDeviceOverrideExampleListeners(t *testing.T) {
	profile, err := os.ReadFile("../docs/device-overrides.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		goos, name string
		gateway    bool
	}{
		{"linux", "example-home-gateway", true},
		{"linux", "another-gateway", false},
		{"darwin", "example-home-gateway", false},
		{"darwin", "Example MacBook Pro", false},
		{"android", "", false},
	} {
		t.Run(tc.goos+"/"+tc.name, func(t *testing.T) {
			b, err := ExpandDeviceOverrides(profile, tc.goos, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := UnmarshalRawConfig(b)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.gateway {
				if cfg.AllowLan || cfg.MixedPort != 7893 || len(cfg.Listeners) != 0 ||
					cfg.ExternalController != "127.0.0.1:9090" || cfg.ExternalControllerTLS != "" ||
					!cfg.DNS.Enable || cfg.DNS.Listen != "127.0.0.1:7874" {
					t.Fatalf("example has unexpected non-gateway listeners: %#v %d %q %q %q", cfg.Listeners, cfg.MixedPort, cfg.ExternalController, cfg.ExternalControllerTLS, cfg.DNS.Listen)
				}
				return
			}
			if cfg.MixedPort != 0 || cfg.ExternalController != "" || cfg.ExternalControllerTLS != "192.0.2.1:9090" ||
				!cfg.DNS.Enable || cfg.DNS.Listen != "192.0.2.1:7874" ||
				cfg.TLS.ClientAuthType != "require-and-verify" || cfg.TLS.Certificate == "" ||
				cfg.TLS.PrivateKey == "" || cfg.TLS.ClientAuthCert == "" {
				t.Fatalf("gateway must select HTTPS with mTLS, turn off the base mixed proxy, and bind DNS to the configured address")
			}
			if len(cfg.Listeners) != 1 {
				t.Fatalf("gateway must use one named mixed listener, got %v", cfg.Listeners)
			}
			mixed := cfg.Listeners[0]
			if mixed["type"] != "mixed" || mixed["listen"] != "192.0.2.1" || mixed["port"] != 7893 ||
				mixed["udp"] != false || mixed["client-auth-type"] != "require-and-verify" ||
				mixed["certificate"] != cfg.TLS.Certificate || mixed["private-key"] != cfg.TLS.PrivateKey ||
				mixed["client-auth-cert"] != cfg.TLS.ClientAuthCert {
				t.Fatalf("gateway mixed listener does not share the controller TLS setup or could expose unauthenticated UDP: %v", mixed)
			}
		})
	}
}
