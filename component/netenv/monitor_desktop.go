//go:build !android && !ios && (darwin || linux || windows)

package netenv

import (
	"github.com/metacubex/mihomo/log"

	tun "github.com/metacubex/sing-tun"
)

func monitorNetworkChanges(fn func()) func() {
	monitor, err := tun.NewNetworkUpdateMonitor(log.SingLogger)
	if err != nil {
		log.Warnln("[Subnet] network monitor unavailable: %v", err)
		return func() {}
	}
	monitor.RegisterCallback(fn)
	if err = monitor.Start(); err != nil {
		_ = monitor.Close()
		log.Warnln("[Subnet] network monitor failed: %v", err)
		return func() {}
	}
	return func() { _ = monitor.Close() }
}
