//go:build darwin && !ios

package netenv

import (
	"context"
	"fmt"
	"strings"
)

func platformDetectionSupported() bool { return true }
func detectPlatform(ctx context.Context, preferred string) (Snapshot, error) {
	routes := readCommand(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet")
	name, gateway := parseDarwinRoutes(routes, preferred)
	if name == "" {
		v6 := readCommand(ctx, "/usr/sbin/netstat", "-rn", "-f", "inet6")
		name, gateway = parseDarwinRoutes(v6, preferred)
		if routes == "" && v6 == "" {
			return Snapshot{}, fmt.Errorf("physical route query failed")
		}
	}
	if preferred != "" {
		name = preferred
	}
	if name == "" {
		return Snapshot{State: None}, nil
	}
	f := parseDarwinSummary(readCommand(ctx, "/usr/sbin/ipconfig", "getsummary", name))
	f.Interface = name
	f.Gateway = gateway
	if f.Type == "" {
		ports := readCommand(ctx, "/usr/sbin/networksetup", "-listallhardwareports")
		for _, section := range strings.Split(ports, "\n\n") {
			if strings.Contains(section, "Device: "+name+"\n") || strings.HasSuffix(section, "Device: "+name) {
				if strings.Contains(section, "Wi-Fi") {
					f.Type = "wifi"
				} else if strings.Contains(section, "Ethernet") {
					f.Type = "wired"
				}
			}
		}
	}
	f.DHCPServer = readCommand(ctx, "/usr/sbin/ipconfig", "getoption", name, "server_identifier")
	if f.Gateway == "" {
		f.Gateway = readCommand(ctx, "/usr/sbin/ipconfig", "getoption", name, "router")
	}
	if f.Gateway != "" {
		f.GatewayMAC = parseDarwinARP(readCommand(ctx, "/usr/sbin/arp", "-n", f.Gateway), f.Gateway, name)
		if f.GatewayMAC == "" {
			f.GatewayMAC = parseDarwinARP(readCommand(ctx, "/usr/sbin/arp", "-n", "-a"), f.Gateway, name)
		}
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	return detectedSnapshot(f), nil
}
