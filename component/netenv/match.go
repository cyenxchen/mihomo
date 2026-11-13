package netenv

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type State string

const (
	Unknown State = "unknown"
	None    State = "none"
	Online  State = "online"
)

type (
	Fields struct {
		Type       string `json:"type,omitempty"`
		Interface  string `json:"interface,omitempty"`
		SSID       string `json:"ssid,omitempty"`
		BSSID      string `json:"bssid,omitempty"`
		Gateway    string `json:"gateway,omitempty"`
		GatewayMAC string `json:"gateway-mac,omitempty"`
		DHCPServer string `json:"dhcp-server,omitempty"`
	}
	Snapshot struct {
		State State `json:"state"`
		Fields
	}
	Match struct {
		Fields
		Proxy string
	}
	Selection struct {
		Proxy   string
		Index   int
		Missing []string
	}
)

var fieldNames = []string{"type", "interface", "ssid", "bssid", "gateway", "gateway-mac", "dhcp-server"}

func (f Fields) values() []string {
	return []string{f.Type, f.Interface, f.SSID, f.BSSID, f.Gateway, f.GatewayMAC, f.DHCPServer}
}

func (f *Fields) pointers() []*string {
	return []*string{&f.Type, &f.Interface, &f.SSID, &f.BSSID, &f.Gateway, &f.GatewayMAC, &f.DHCPServer}
}

func normalizeMAC(value string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(value, "-", ":"), ":")
	if len(parts) != 6 {
		return "", fmt.Errorf("invalid MAC: %q", value)
	}
	bytes := make(net.HardwareAddr, 6)
	for i, p := range parts {
		if len(p) == 0 || len(p) > 2 {
			return "", fmt.Errorf("invalid MAC: %q", value)
		}
		n, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return "", err
		}
		bytes[i] = byte(n)
	}
	return bytes.String(), nil
}

func ParseFields(raw map[string]any, report bool) (Fields, error) {
	var f Fields
	ptrs := f.pointers()
	for key, v := range raw {
		idx := -1
		for i, k := range fieldNames {
			if key == k {
				idx = i
				break
			}
		}
		if idx < 0 {
			return f, fmt.Errorf("unknown network field %q", key)
		}
		if v == nil && report {
			continue
		}
		value, ok := v.(string)
		if !ok || value == "" {
			return f, fmt.Errorf("%s must be a nonempty string", key)
		}
		var err error
		switch key {
		case "type":
			if value != "wifi" && value != "wired" && value != "cellular" && !(report && value == "none") {
				return f, fmt.Errorf("invalid network type %q", value)
			}
		case "ssid":
			if !utf8.ValidString(value) || len(value) > 32 {
				return f, fmt.Errorf("ssid exceeds 32 bytes or is not UTF-8")
			}
		case "interface":
			if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 || strings.ContainsFunc(value, unicode.IsControl) {
				return f, fmt.Errorf("invalid interface name")
			}
		case "bssid", "gateway-mac":
			value, err = normalizeMAC(value)
		case "gateway", "dhcp-server":
			var ip netip.Addr
			ip, err = netip.ParseAddr(value)
			if err == nil {
				if ip.Zone() != "" {
					err = fmt.Errorf("scoped IP is not supported")
				} else {
					value = ip.Unmap().String()
				}
			}
		}
		if err != nil {
			return f, fmt.Errorf("%s: %w", key, err)
		}
		*ptrs[idx] = value
	}
	if (f.Type == "wired" || f.Type == "cellular") && (f.SSID != "" || f.BSSID != "") {
		return f, fmt.Errorf("%s conflicts with ssid/bssid", f.Type)
	}
	return f, nil
}

func ParseMatches(raw any) ([]Match, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("subnet requires nonempty matches")
	}
	out := make([]Match, 0, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("match %d must be a mapping", i+1)
		}
		proxy, ok := m["proxy"].(string)
		if !ok || proxy == "" {
			return nil, fmt.Errorf("match %d requires proxy", i+1)
		}
		conditions := make(map[string]any, len(m))
		for k, v := range m {
			if k != "proxy" {
				conditions[k] = v
			}
		}
		if len(conditions) == 0 {
			return nil, fmt.Errorf("match %d requires a condition", i+1)
		}
		f, err := ParseFields(conditions, false)
		if err != nil {
			return nil, fmt.Errorf("match %d: %w", i+1, err)
		}
		out = append(out, Match{Fields: f, Proxy: proxy})
	}
	return out, nil
}

func Select(rules []Match, fallback string, s Snapshot) Selection {
	out := Selection{Proxy: fallback}
	if s.State == Unknown || s.State == "" {
		if len(rules) > 0 {
			out.Proxy = rules[0].Proxy
			out.Index = -1
		}
		return out
	}
	if s.State == None {
		return out
	}
	actual := s.Fields.values()
	for i, r := range rules {
		if (r.SSID != "" || r.BSSID != "") && s.Type != "" && s.Type != "wifi" {
			continue
		}
		matches := true
		for j, want := range r.Fields.values() {
			if want == "" {
				continue
			}
			if actual[j] == "" {
				out.Missing = append(out.Missing, fmt.Sprintf("match %d: %s unavailable", i+1, fieldNames[j]))
			}
			if actual[j] != want {
				matches = false
			}
		}
		if matches {
			out.Proxy = r.Proxy
			out.Index = i + 1
			return out
		}
	}
	return out
}
