package inbound

import (
	"net"
	"net/netip"
	"slices"

	"github.com/metacubex/mihomo/common/atomic"
	C "github.com/metacubex/mihomo/constant"
)

var lanAllowedIPs atomic.TypedValue[[]netip.Prefix]
var lanDisAllowedIPs atomic.TypedValue[[]netip.Prefix]

func SetAllowedIPs(prefixes []netip.Prefix) {
	lanAllowedIPs.Store(slices.Clone(prefixes))
}

func SetDisAllowedIPs(prefixes []netip.Prefix) {
	lanDisAllowedIPs.Store(slices.Clone(prefixes))
}

func AllowedIPs() []netip.Prefix {
	return slices.Clone(lanAllowedIPs.Load())
}

func DisAllowedIPs() []netip.Prefix {
	return slices.Clone(lanDisAllowedIPs.Load())
}

func IsRemoteAddrDisAllowed(addr net.Addr) bool {
	m := C.Metadata{}
	if err := m.SetRemoteAddr(addr); err != nil {
		return false
	}
	ipAddr := m.AddrPort().Addr()
	if ipAddr.IsValid() {
		return isAllowed(ipAddr) && !isDisAllowed(ipAddr)
	}
	return false
}

func isAllowed(addr netip.Addr) bool {
	return prefixesContains(lanAllowedIPs.Load(), addr)
}

func isDisAllowed(addr netip.Addr) bool {
	return prefixesContains(lanDisAllowedIPs.Load(), addr)
}
