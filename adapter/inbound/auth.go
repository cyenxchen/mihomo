package inbound

import (
	"net"
	"net/netip"
	"slices"

	"github.com/metacubex/mihomo/common/atomic"
	C "github.com/metacubex/mihomo/constant"
)

var skipAuthPrefixes atomic.TypedValue[[]netip.Prefix]

func SetSkipAuthPrefixes(prefixes []netip.Prefix) {
	skipAuthPrefixes.Store(slices.Clone(prefixes))
}

func SkipAuthPrefixes() []netip.Prefix {
	return slices.Clone(skipAuthPrefixes.Load())
}

func SkipAuthRemoteAddr(addr net.Addr) bool {
	m := C.Metadata{}
	if err := m.SetRemoteAddr(addr); err != nil {
		return false
	}
	return skipAuth(m.AddrPort().Addr())
}

func SkipAuthRemoteAddress(addr string) bool {
	m := C.Metadata{}
	if err := m.SetRemoteAddress(addr); err != nil {
		return false
	}
	return skipAuth(m.AddrPort().Addr())
}

func skipAuth(addr netip.Addr) bool {
	return prefixesContains(skipAuthPrefixes.Load(), addr)
}
