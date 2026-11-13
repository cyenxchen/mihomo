package netenv

import (
	"strings"
	"testing"
	"time"
)

func TestReviewK4RejectsUnboundAndStaleLeases(t *testing.T) {
	for _, mode := range []string{"nmcli-old-uuid", "stale-systemd", "wrong-dhclient-interface"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			src := linuxLeaseSource{nmcliAvailable: mode == "nmcli-old-uuid", upSince: time.Unix(100, 0), modified: func(string) time.Time {
				if mode == "wrong-dhclient-interface" {
					return time.Unix(101, 0)
				}
				return time.Unix(99, 0)
			}, command: func(_ string, args ...string) string {
				if strings.Contains(strings.Join(args, " "), "CON-UUID") {
					return "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
				}
				return ""
			}, glob: func(string) []string { calls++; return []string{"/var/lib/dhcp/dhclient.eth0.leases"} }, read: func(path string) string {
				if mode == "stale-systemd" || strings.Contains(path, "dhclient") {
					return `lease { interface "eno1"; option dhcp-server-identifier 10.15.200.1; }`
				}
				return ""
			}}
			if got := linuxDHCPServer("eth0", 3, src); got != "" {
				t.Fatalf("used stale/unbound lease: %s", got)
			}
			if src.nmcliAvailable && calls != 0 {
				t.Fatal("nmcli path searched historical leases")
			}
		})
	}
}

func TestReviewK4CurrentConnectionLease(t *testing.T) {
	src := linuxLeaseSource{nmcliAvailable: true, upSince: time.Unix(100, 0), modified: func(string) time.Time { return time.Unix(101, 0) }, command: func(_ string, args ...string) string {
		if strings.Contains(strings.Join(args, " "), "CON-UUID") {
			return "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		}
		return ""
	}, read: func(path string) string {
		if path == "/var/lib/NetworkManager/internal-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-eth0.lease" {
			return "SERVER_ADDRESS=192.168.1.1"
		}
		return ""
	}}
	if got := linuxDHCPServer("eth0", 3, src); got != "192.168.1.1" {
		t.Fatal(got)
	}
	src.upSince = time.Time{}
	if got := linuxDHCPServer("eth0", 3, src); got != "" {
		t.Fatal("unproven up time accepted")
	}
}
