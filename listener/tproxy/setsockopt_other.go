//go:build !linux

package tproxy

import (
	"errors"
	"syscall"
)

const SupportUDP = false

func setsockopt(rc syscall.RawConn, addr string) error {
	return errors.New("not supported on current platform")
}
