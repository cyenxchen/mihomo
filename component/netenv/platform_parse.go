package netenv

import (
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

func resolveInterfaceHint(configured, found string) string {
	if configured != "" {
		return configured
	}
	if found == "<invalid>" || found == "<nil>" {
		return ""
	}
	return found
}

func physicalInterface(name string) bool {
	for _, prefix := range []string{"utun", "tun", "tap", "wg", "tailscale", "docker", "veth", "lo", "ppp", "ipsec", "awdl", "llw", "gif", "stf"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	return name != ""
}

func cleanPlatformValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "<redacted>" || value == "<unknown ssid>" {
		return ""
	}
	return strings.Trim(value, "\"")
}

func parseDarwinSummary(text string) Fields {
	var f Fields
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = cleanPlatformValue(value)
		switch strings.TrimSpace(key) {
		case "InterfaceType":
			switch value {
			case "WiFi":
				f.Type = "wifi"
			case "Ethernet":
				f.Type = "wired"
			case "Cellular":
				f.Type = "cellular"
			}
		case "SSID":
			f.SSID = value
		case "BSSID":
			f.BSSID, _ = normalizeMAC(value)
		}
	}
	return f
}

func parseDarwinRoutes(text, preferred string) (string, string) {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(line)
		if len(parts) < 4 || parts[0] != "default" {
			continue
		}
		name := parts[3]
		if preferred != "" && name != preferred || preferred == "" && !physicalInterface(name) {
			continue
		}
		gateway := strings.Split(parts[1], "%")[0]
		if ip, err := netip.ParseAddr(gateway); err == nil {
			return name, ip.String()
		}
	}
	return "", ""
}

func parseNeighbor(text, gateway string, iface ...string) string {
	if gateway == "" {
		return ""
	}
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 6 && strings.HasPrefix(parts[1], "0x") {
			flags, err := strconv.ParseUint(parts[2], 0, 32)
			if err != nil || flags&2 == 0 || len(iface) > 0 && parts[5] != iface[0] {
				continue
			}
		}
		if strings.Contains(line, "INCOMPLETE") || strings.Contains(line, "FAILED") {
			continue
		}
		if len(iface) > 0 {
			mismatch := false
			for i, p := range parts {
				if p == "dev" && i+1 < len(parts) && parts[i+1] != iface[0] {
					mismatch = true
				}
			}
			if mismatch {
				continue
			}
		}
		found := false
		for _, p := range parts {
			if strings.Trim(p, "()") == gateway {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		for _, p := range parts {
			if strings.ContainsAny(p, ":-") {
				if mac := usableNeighborMAC(p); mac != "" {
					return mac
				}
			}
		}
	}
	return ""
}

func parseLinuxRoutes(text, preferred string) (string, string) {
	best := uint64(^uint32(0))
	name, gateway := "", ""
	for _, line := range strings.Split(text, "\n") {
		p := strings.Fields(line)
		if len(p) < 8 || p[1] != "00000000" {
			continue
		}
		if preferred != "" && p[0] != preferred || preferred == "" && !physicalInterface(p[0]) {
			continue
		}
		flags, err := strconv.ParseUint(p[3], 16, 32)
		if err != nil || flags&1 == 0 {
			continue
		}
		metric, err := strconv.ParseUint(p[6], 10, 32)
		if err != nil || metric >= best {
			continue
		}
		value, err := strconv.ParseUint(p[2], 16, 32)
		if err != nil {
			continue
		}
		b := [4]byte{}
		binary.LittleEndian.PutUint32(b[:], uint32(value))
		best = metric
		name = p[0]
		gateway = netip.AddrFrom4(b).String()
	}
	return name, gateway
}

func parseLinuxIPv6Routes(text, preferred string) (string, string) {
	best := uint64(^uint32(0))
	name, gateway := "", ""
	for _, line := range strings.Split(text, "\n") {
		p := strings.Fields(line)
		if len(p) < 10 || p[0] != strings.Repeat("0", 32) || p[1] != "00" {
			continue
		}
		n := p[len(p)-1]
		if preferred != "" && n != preferred || preferred == "" && !physicalInterface(n) {
			continue
		}
		metric, err := strconv.ParseUint(p[5], 16, 32)
		if err != nil || metric >= best {
			continue
		}
		raw, err := hex.DecodeString(p[4])
		if err != nil || len(raw) != 16 {
			continue
		}
		var b [16]byte
		copy(b[:], raw)
		ip := netip.AddrFrom16(b)
		if ip.IsUnspecified() {
			continue
		}
		name = n
		gateway = ip.String()
		best = metric
	}
	return name, gateway
}

func parseLinuxLease(text string) string {
	latest := ""
	for _, line := range strings.Split(text, "\n") {
		if !strings.Contains(line, "SERVER_ADDRESS") && !strings.Contains(line, "dhcp_server_identifier") && !strings.Contains(line, "dhcp-server-identifier") {
			continue
		}
		line = strings.NewReplacer("=", " ", ";", " ", "'", " ", "\"", " ").Replace(line)
		for _, p := range strings.Fields(line) {
			if ip, err := netip.ParseAddr(p); err == nil {
				latest = ip.Unmap().String()
			}
		}
	}
	return latest
}

func parseWindowsWifi(data []byte) (string, string) {
	if len(data) < 566 {
		return "", ""
	}
	size := binary.LittleEndian.Uint32(data[520:524])
	if size > 32 {
		return "", ""
	}
	return string(data[524 : 524+size]), net.HardwareAddr(data[560:566]).String()
}

func windowsInterfaceType(kind uint32) string {
	switch kind {
	case 6:
		return "wired"
	case 71:
		return "wifi"
	case 243, 244:
		return "cellular"
	}
	return ""
}

// nmcli terse output escapes both field separators and literal backslashes.
func parseLinuxNMCLIWifi(output string) (string, string) {
	for _, line := range strings.Split(output, "\n") {
		fields := []string{}
		var field strings.Builder
		escaped := false
		for _, c := range line {
			if escaped {
				field.WriteRune(c)
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == ':' {
				fields = append(fields, field.String())
				field.Reset()
			} else {
				field.WriteRune(c)
			}
		}
		fields = append(fields, field.String())
		if len(fields) == 3 && fields[0] == "yes" {
			return fields[1], fields[2]
		}
	}
	return "", ""
}

func parseDarwinARP(text, gateway, iface string) string {
	for _, line := range strings.Split(text, "\n") {
		parts := strings.Fields(line)
		for i, p := range parts {
			if p == "on" && i+1 < len(parts) && parts[i+1] == iface {
				if mac := parseNeighbor(line, gateway); mac != "" {
					return mac
				}
			}
		}
	}
	return ""
}

func linuxWiFiListArgs(name string) []string {
	return []string{"-t", "-f", "ACTIVE,SSID,BSSID", "device", "wifi", "list", "ifname", name, "--rescan", "no"}
}

func usableNeighborMAC(value string) string {
	mac, err := normalizeMAC(value)
	if err != nil || mac == "00:00:00:00:00:00" {
		return ""
	}
	return mac
}

func windowsNeighborMAC(value string, state uint32) string {
	if state <= 1 {
		return ""
	}
	return usableNeighborMAC(value)
}
