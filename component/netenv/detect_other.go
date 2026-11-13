//go:build android || ios || (!darwin && !linux && !windows)

package netenv

import (
	"context"
	"errors"
)

func platformDetectionSupported() bool { return false }
func detectPlatform(context.Context, string) (Snapshot, error) {
	return Snapshot{}, errors.New("network self-detection unsupported")
}
