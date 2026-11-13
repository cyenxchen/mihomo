//go:build android || ios || (!darwin && !linux && !windows)

package netenv

func monitorNetworkChanges(func()) func() { return func() {} }
