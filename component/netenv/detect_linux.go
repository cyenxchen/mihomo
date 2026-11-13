//go:build linux && !android

package netenv

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/log"
)

var missingLinuxInterfaces sync.Map

func platformDetectionSupported() bool { return true }
func readNetFile(path string) string   { data, _ := os.ReadFile(path); return string(data) }
func detectPlatform(ctx context.Context, preferred string) (Snapshot, error) {
	routes, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return Snapshot{}, err
	}
	name, gateway := parseLinuxRoutes(string(routes), preferred)
	if name == "" {
		name, gateway = parseLinuxIPv6Routes(readNetFile("/proc/net/ipv6_route"), preferred)
	}
	if preferred != "" {
		name = preferred
	}
	if name == "" {
		return Snapshot{State: None}, nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		if preferred != "" {
			if _, statErr := os.Stat(filepath.Join("/sys/class/net", name)); os.IsNotExist(statErr) {
				if _, seen := missingLinuxInterfaces.LoadOrStore(name, true); !seen {
					log.Warnln("[Subnet] configured interface %s does not exist; network is none", name)
				}
				return Snapshot{State: None}, nil
			}
		}
		return Snapshot{}, err
	}
	if iface.Flags&net.FlagUp == 0 {
		return Snapshot{State: None}, nil
	}
	f := Fields{Interface: name, Gateway: gateway}
	base := filepath.Join("/sys/class/net", name)
	if _, err := os.Stat(filepath.Join(base, "wireless")); err == nil {
		f.Type = "wifi"
	} else if _, err := os.Stat(filepath.Join(base, "phy80211")); err == nil {
		f.Type = "wifi"
	} else if strings.Contains(readNetFile(filepath.Join(base, "device/uevent")), "wwan") || strings.HasPrefix(name, "wwan") {
		f.Type = "cellular"
	} else if strings.TrimSpace(readNetFile(filepath.Join(base, "type"))) == "1" {
		f.Type = "wired"
	}
	if f.Type == "wifi" {
		output := readCommand(ctx, "/usr/sbin/iw", "dev", name, "link")
		if output == "" {
			output = readCommand(ctx, "/usr/bin/iw", "dev", name, "link")
		}
		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if value, ok := strings.CutPrefix(line, "SSID: "); ok {
				f.SSID = value
			}
			if strings.HasPrefix(line, "Connected to ") {
				parts := strings.Fields(line)
				if len(parts) > 2 {
					f.BSSID = parts[2]
				}
			}
		}
		if f.SSID == "" {
			out := readCommand(ctx, "/usr/bin/nmcli", linuxWiFiListArgs(name)...)
			f.SSID, f.BSSID = parseLinuxNMCLIWifi(out)
		}
	}
	f.GatewayMAC = parseNeighbor(readNetFile("/proc/net/arp"), gateway, name)
	_, nmcliErr := os.Stat("/usr/bin/nmcli")
	carrier := strings.TrimSpace(readNetFile(filepath.Join(base, "carrier")))
	changes := strings.TrimSpace(readNetFile(filepath.Join(base, "carrier_changes")))
	upSince := linuxLeaseUpSince(name, strconv.Itoa(iface.Index)+":"+changes, carrier == "1" && changes != "", time.Now())
	f.DHCPServer = linuxDHCPServer(name, iface.Index, linuxLeaseSource{
		command: func(path string, args ...string) string { return readCommand(ctx, path, args...) }, read: readNetFile, nmcliAvailable: nmcliErr == nil, upSince: upSince,
		modified: func(path string) time.Time {
			info, err := os.Stat(path)
			if err != nil {
				return time.Time{}
			}
			return info.ModTime()
		},
	})
	if f.GatewayMAC == "" && gateway != "" {
		f.GatewayMAC = parseNeighbor(readCommand(ctx, "/usr/sbin/ip", "neigh", "show", "to", gateway, "dev", name), gateway, name)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return detectedSnapshot(f), nil
}
