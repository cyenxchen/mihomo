package netenv

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// All filesystem and process inputs are injectable; unknown freshness is rejected.
type linuxLeaseSource struct {
	command        func(string, ...string) string
	read           func(string) string
	glob           func(string) []string // Kept as a test sentinel: historical globbing is forbidden.
	modified       func(string) time.Time
	nmcliAvailable bool
	upSince        time.Time
}

var (
	linuxConnectionUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
	linuxLeaseBlock     = regexp.MustCompile(`(?s)lease\s*\{([^}]*)\}`)
	linuxLeaseInterface = regexp.MustCompile(`\binterface\s+"([^"]+)"\s*;`)
)

func linuxDHCPServer(name string, index int, src linuxLeaseSource) string {
	fresh := func(path string) string {
		if src.upSince.IsZero() || src.modified == nil {
			return ""
		}
		modified := src.modified(path)
		if modified.IsZero() || modified.Before(src.upSince) {
			return ""
		}
		return src.read(path)
	}
	if src.nmcliAvailable {
		if server := parseLinuxLease(src.command("/usr/bin/nmcli", "-f", "DHCP4.OPTION", "device", "show", name)); server != "" {
			return server
		}
		uuid := strings.TrimSpace(src.command("/usr/bin/nmcli", "-g", "GENERAL.CON-UUID", "device", "show", name))
		if !linuxConnectionUUID.MatchString(uuid) {
			return ""
		}
		for _, kind := range []string{"internal", "dhclient"} {
			path := fmt.Sprintf("/var/lib/NetworkManager/%s-%s-%s.lease", kind, uuid, name)
			if server := parseLinuxLease(fresh(path)); server != "" {
				return server
			}
		}
		return ""
	}
	// A systemd-networkd lease filename identifies the current interface index.
	if server := parseLinuxLease(fresh(fmt.Sprintf("/run/systemd/netif/leases/%d", index))); server != "" {
		return server
	}
	for _, path := range []string{"/var/lib/dhcp/dhclient." + name + ".leases", "/var/lib/dhcp/dhclient-" + name + ".leases", "/var/lib/dhcp/dhclient.leases"} {
		blocks := linuxLeaseBlock.FindAllStringSubmatch(fresh(path), -1)
		if len(blocks) == 0 {
			continue
		}
		block := blocks[len(blocks)-1][1]
		iface := linuxLeaseInterface.FindStringSubmatch(block)
		if len(iface) != 2 || iface[1] != name {
			continue
		}
		if server := parseLinuxLease(block); server != "" {
			return server
		}
	}
	return ""
}

// The first observation is a conservative bound, not an invented kernel up time.
// A lease older than it cannot be proven to belong to the current carrier epoch.
var linuxLeaseObservations = struct {
	sync.Mutex
	epochs map[string]struct {
		token string
		since time.Time
	}
}{epochs: make(map[string]struct {
	token string
	since time.Time
})}

func linuxLeaseUpSince(name, token string, up bool, now time.Time) time.Time {
	linuxLeaseObservations.Lock()
	defer linuxLeaseObservations.Unlock()
	if !up || token == "" {
		delete(linuxLeaseObservations.epochs, name)
		return time.Time{}
	}
	old, ok := linuxLeaseObservations.epochs[name]
	if !ok || old.token != token {
		old = struct {
			token string
			since time.Time
		}{token, now}
		linuxLeaseObservations.epochs[name] = old
	}
	return old.since
}
