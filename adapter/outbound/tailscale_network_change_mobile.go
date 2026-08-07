//go:build (android || darwin) && with_gvisor && !no_tailscale

package outbound

import "github.com/metacubex/tailscale/net/netmon"

func updateTailscaleDefaultRouteInterface(defaultInterface string) {
	// Android and Apple frontends cannot rely on Go to discover the active
	// route, so feed the platform hint before waking each tsnet monitor.
	netmon.UpdateLastKnownDefaultRouteInterface(defaultInterface)
}
