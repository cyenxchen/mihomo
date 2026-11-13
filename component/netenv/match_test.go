package netenv

import (
	"testing"
)

func TestSubnetMatching(t *testing.T) {
	rules, err := ParseMatches([]any{
		map[string]any{"ssid": "GOLDWIND", "proxy": "DIRECT"},
		map[string]any{"dhcp-server": "10.15.200.1", "gateway-mac": "0:8:e3:ff:fc:4", "proxy": "LAN"},
		map[string]any{"type": "cellular", "proxy": "CELL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		s     Snapshot
		want  string
		index int
	}{
		{"unknown", Snapshot{}, "DIRECT", -1},
		{"none", Snapshot{State: None}, "TAIL", 0},
		{"first", Snapshot{State: Online, Fields: Fields{Type: "wifi", SSID: "GOLDWIND", DHCPServer: "10.15.200.1", GatewayMAC: "00:08:e3:ff:fc:04"}}, "DIRECT", 1},
		{"unavailable", Snapshot{State: Online, Fields: Fields{Type: "wifi", DHCPServer: "10.15.200.1", GatewayMAC: "00:08:e3:ff:fc:04"}}, "LAN", 2},
		{"all conditions", Snapshot{State: Online, Fields: Fields{DHCPServer: "10.15.200.1"}}, "TAIL", 0},
		{"implicit wifi", Snapshot{State: Online, Fields: Fields{Type: "cellular", SSID: "GOLDWIND"}}, "CELL", 3},
		{"case sensitive", Snapshot{State: Online, Fields: Fields{SSID: "goldwind"}}, "TAIL", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Select(rules, "TAIL", tc.s)
			if got.Proxy != tc.want || got.Index != tc.index {
				t.Fatalf("got %+v, want %s/%d", got, tc.want, tc.index)
			}
		})
	}
}

func TestSubnetValidation(t *testing.T) {
	for _, input := range []map[string]any{
		{"proxy": "DIRECT"},
		{"ssid": "x"},
		{"proxy": "DIRECT", "sssid": "x"},
		{"proxy": "DIRECT", "ssid": ""},
		{"proxy": "DIRECT", "ssid": []any{"x"}},
		{"proxy": "DIRECT", "type": "wired", "ssid": "x"},
		{"proxy": "DIRECT", "type": "cellular", "bssid": "00:11:22:33:44:55"},
		{"proxy": "DIRECT", "type": "ethernet"},
		{"proxy": "DIRECT", "gateway": "10.0.0.0/8"},
		{"proxy": "DIRECT", "bssid": "not-mac"},
	} {
		if _, err := ParseMatches([]any{input}); err == nil {
			t.Errorf("accepted invalid match: %v", input)
		}
	}
	for _, key := range []string{"type", "ssid", "bssid", "dhcp-server", "gateway", "gateway-mac", "interface"} {
		val := map[string]string{"type": "wifi", "ssid": "X", "bssid": "00-08-E3-FF-FC-04", "dhcp-server": "10.0.0.1", "gateway": "2001:0db8::1", "gateway-mac": "0:8:e3:ff:fc:4", "interface": "en0"}[key]
		rules, err := ParseMatches([]any{map[string]any{key: val, "proxy": "yes"}})
		if err != nil {
			t.Fatal(err)
		}
		fields, err := ParseFields(map[string]any{key: val}, false)
		if err != nil {
			t.Fatal(err)
		}
		if got := Select(rules, "no", Snapshot{State: Online, Fields: fields}); got.Proxy != "yes" {
			t.Errorf("%s did not match: %+v", key, got)
		}
		if got := Select(rules, "no", Snapshot{State: Online}); got.Proxy != "no" {
			t.Errorf("%s missing matched", key)
		}
	}
}
