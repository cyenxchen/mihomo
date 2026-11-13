package netenv

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestSubnetDesktopParsers(t *testing.T) {
	f := parseDarwinSummary("InterfaceType : WiFi\nSSID : <redacted>\nBSSID : <redacted>\n")
	if f.Type != "wifi" || f.SSID != "" || f.BSSID != "" {
		t.Fatalf("darwin redacted: %+v", f)
	}
	f = parseDarwinSummary("InterfaceType : Ethernet\n")
	if f.Type != "wired" {
		t.Fatal("ethernet type not detected")
	}
	name, gw := parseDarwinRoutes("Destination Gateway Flags Netif Expire\ndefault 198.18.0.1 UGSc utun9\ndefault 10.15.67.254 UGSc en0\n", "")
	if name != "en0" || gw != "10.15.67.254" {
		t.Fatalf("physical route: %s %s", name, gw)
	}
	if got := parseNeighbor("? (10.15.67.254) at 0:8:e3:ff:fc:4 on en0 ifscope [ethernet]", "10.15.67.254"); got != "00:08:e3:ff:fc:04" {
		t.Fatal(got)
	}
}

func TestSubnetLinuxParser(t *testing.T) {
	name, gw := parseLinuxRoutes("Iface Destination Gateway Flags RefCnt Use Metric Mask\ntun0 00000000 010012C6 0003 0 0 1 00000000\neth0 00000000 0101A8C0 0003 0 0 100 00000000\n", "")
	if name != "eth0" || gw != "192.168.1.1" {
		t.Fatalf("linux route: %s %s", name, gw)
	}
	for _, sample := range []string{"SERVER_ADDRESS=10.15.200.1\n", "DHCP4.OPTION[2]: dhcp_server_identifier = 10.15.200.1\n", "option dhcp-server-identifier 10.15.200.1;", "dhcp_server_identifier='10.15.200.1'"} {
		if got := parseLinuxLease(sample); got != "10.15.200.1" {
			t.Fatalf("lease %q: %q", sample, got)
		}
	}
}

func TestSubnetWindowsParser(t *testing.T) {
	wifi := make([]byte, 588)
	binary.LittleEndian.PutUint32(wifi[520:], 8)
	copy(wifi[524:], "GOLDWIND")
	copy(wifi[560:], []byte{0, 8, 0xe3, 0xff, 0xfc, 4})
	ssid, bssid := parseWindowsWifi(wifi)
	if ssid != "GOLDWIND" || bssid != "00:08:e3:ff:fc:04" {
		t.Fatalf("windows WLAN: %q %q", ssid, bssid)
	}
	if a, b := parseWindowsWifi(wifi[:20]); a != "" || b != "" {
		t.Fatal("short WLAN buffer accepted")
	}
	if resolveInterfaceHint("", "<invalid>") != "" || resolveInterfaceHint("en3", "en0") != "en3" {
		t.Fatal("interface preference")
	}
}

func TestSubnetNeighborExactAddress(t *testing.T) {
	text := "10.1.1.10 dev eth0 lladdr 00:11:22:33:44:55 REACHABLE\n10.1.1.1 dev eth0 lladdr 00:aa:bb:cc:dd:ee REACHABLE"
	if got := parseNeighbor(text, "10.1.1.1"); got != "00:aa:bb:cc:dd:ee" {
		t.Fatalf("matched different gateway: %s", got)
	}
	if got := parseNeighbor(text, ""); got != "" {
		t.Fatalf("invented unknown gateway MAC: %s", got)
	}
}

func TestSubnetLeaseUsesLatestBlock(t *testing.T) {
	if got := parseLinuxLease("option dhcp-server-identifier 10.1.1.1;\noption dhcp-server-identifier 10.2.2.2;"); got != "10.2.2.2" {
		t.Fatal(got)
	}
}

func TestSubnetNMCLIEscapedSSID(t *testing.T) {
	ssid, bssid := parseLinuxNMCLIWifi(`no:OTHER:00\:11\:22\:33\:44\:55
yes:Corp\:WiFi:00\:08\:e3\:ff\:fc\:04`[0:])
	if ssid != "Corp:WiFi" || bssid != "00:08:e3:ff:fc:04" {
		t.Fatalf("escaped nmcli: %q %q", ssid, bssid)
	}
}

func TestSubnetDarwinNeighborInterface(t *testing.T) {
	sample := "? (10.15.67.254) at 00:11:22:33:44:55 on en3 ifscope [ethernet]\n? (10.15.67.254) at 0:8:e3:ff:fc:4 on en0 ifscope [ethernet]"
	if got := parseDarwinARP(sample, "10.15.67.254", "en0"); got != "00:08:e3:ff:fc:04" {
		t.Fatalf("neighbor from wrong interface: %s", got)
	}
}

func TestSubnetNMCLIListDoesNotRequestScan(t *testing.T) {
	if !strings.Contains(strings.Join(linuxWiFiListArgs("wlp0s20f3"), " "), "--rescan no") {
		t.Fatal("nmcli Wi-Fi list can trigger an automatic rescan")
	}
}

func TestSubnetNuc10CapturedNetwork(t *testing.T) {
	// Read-only samples captured on nuc10, Omarchy 7.2.15, 2026-11-13.
	name, gw := parseLinuxRoutes("wlp0s20f3 00000000 FE430F0A 0003 0 0 600 00000000 0 0 0\nutun 000013C6 00000000 0001 0 0 0 FCFFFFFF 0 0 0", "")
	if name != "wlp0s20f3" || gw != "10.15.67.254" {
		t.Fatalf("route: %s %s", name, gw)
	}
	mac := parseNeighbor("10.15.67.254 0x1 0x2 00:08:e3:ff:fc:04 * wlp0s20f3", gw)
	if mac != "00:08:e3:ff:fc:04" {
		t.Fatal(mac)
	}
	dhcp := parseLinuxLease("DHCP4.OPTION[3]: dhcp_server_identifier = 10.15.200.1")
	if dhcp != "10.15.200.1" {
		t.Fatal(dhcp)
	}
}

func TestReviewK5IncompleteNeighborsAreUnavailable(t *testing.T) {
	for _, line := range []string{"10.1.1.1 0x1 0x0 00:11:22:33:44:55 * eth0", "10.1.1.1 0x1 0x2 00:00:00:00:00:00 * eth0", "10.1.1.1 dev eth0 lladdr 00:00:00:00:00:00 INCOMPLETE"} {
		if got := parseNeighbor(line, "10.1.1.1"); got != "" {
			t.Errorf("invalid neighbor accepted: %q -> %s", line, got)
		}
	}
	if got := parseDarwinARP("? (10.1.1.1) at 0:0:0:0:0:0 on en0 ifscope [ethernet]", "10.1.1.1", "en0"); got != "" {
		t.Fatal(got)
	}
}

func TestReviewK5LearningGatewayMACDoesNotExpireReport(t *testing.T) {
	s := NewStore(time.Now)
	snapshot := Snapshot{State: Online, Fields: Fields{Interface: "en0", Gateway: "10.1.1.1"}}
	s.SetDetector(func(context.Context) (Snapshot, error) { return snapshot, nil })
	_ = s.Report(context.Background(), Fields{SSID: "GOLDWIND"}, "rest")
	snapshot.GatewayMAC = "00:08:e3:ff:fc:04"
	_ = s.Detect(context.Background())
	if !s.View().Report.Valid {
		t.Fatal("learning previously missing MAC expired report")
	}
}

func TestReviewK5NeighborDeviceAndWindowsState(t *testing.T) {
	text := "10.1.1.1 0x1 0x2 00:11:22:33:44:55 * eth1\n10.1.1.1 0x1 0x2 00:08:e3:ff:fc:04 * eth0"
	if got := parseNeighbor(text, "10.1.1.1", "eth0"); got != "00:08:e3:ff:fc:04" {
		t.Fatal(got)
	}
	if got := parseNeighbor("10.1.1.1 dev eth1 lladdr 00:11:22:33:44:55 REACHABLE", "10.1.1.1", "eth0"); got != "" {
		t.Fatal(got)
	}
	for _, state := range []uint32{0, 1} {
		if got := windowsNeighborMAC("00:11:22:33:44:55", state); got != "" {
			t.Fatal(got)
		}
	}
	if got := windowsNeighborMAC("00:00:00:00:00:00", 5); got != "" {
		t.Fatal(got)
	}
	if got := windowsNeighborMAC("00:11:22:33:44:55", 5); got != "00:11:22:33:44:55" {
		t.Fatal(got)
	}
	if got := parseDarwinARP("? (10.1.1.1) at (incomplete) on en0 ifscope [ethernet]", "10.1.1.1", "en0"); got != "" {
		t.Fatal(got)
	}
}
