//go:build !android && !darwin && with_gvisor && !no_tailscale

package outbound

func updateTailscaleDefaultRouteInterface(string) {}
