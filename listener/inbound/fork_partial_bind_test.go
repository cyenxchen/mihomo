package inbound_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"

	"github.com/metacubex/mihomo/listener/inbound"
	"github.com/metacubex/mihomo/listener/tproxy"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamedTProxyRollsBackPartialBind(t *testing.T) {
	if !tproxy.SupportUDP {
		t.Skip("requires Linux transparent sockets")
	}
	probe, err := tproxy.NewUDP("127.0.0.1:0", nil)
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		t.Skipf("transparent sockets unavailable: %v", err)
	}
	require.NoError(t, err)
	require.NoError(t, probe.Close())

	firstTCP, firstUDP := reservePartialBindAddress(t)
	secondTCP, secondUDP := reservePartialBindAddress(t)
	named, err := inbound.NewTProxy(&inbound.TProxyOption{
		BaseOption: partialBindBaseOption("partial-tproxy", firstTCP, secondTCP),
		UDP:        true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = named.Close() })
	firstAddress, secondAddress := firstTCP.Addr().String(), secondTCP.Addr().String()
	require.NoError(t, firstTCP.Close())
	require.NoError(t, firstUDP.Close())
	require.NoError(t, secondTCP.Close())

	requireBindConflict(t, named.Listen(nil), "udp", secondAddress)
	assert.Empty(t, named.Address())
	assertBindAvailable(t, "tcp4", firstAddress)
	assertBindAvailable(t, "udp4", firstAddress)
	assertBindAvailable(t, "tcp4", secondAddress)
	require.NoError(t, secondUDP.Close())
	require.NoError(t, named.Listen(nil))
	assert.Equal(t, named.RawAddress(), named.Address())
	assertBindOccupied(t, firstAddress, secondAddress)
}

func TestNamedShadowSocksRollsBackPartialBind(t *testing.T) {
	for _, cipher := range []string{"aes-128-gcm", "aes-128-cfb"} {
		t.Run(cipher, func(t *testing.T) {
			firstTCP, firstUDP := reservePartialBindAddress(t)
			secondTCP, secondUDP := reservePartialBindAddress(t)
			named, err := inbound.NewShadowSocks(&inbound.ShadowSocksOption{
				BaseOption: partialBindBaseOption("partial-shadowsocks", firstTCP, secondTCP),
				Cipher:     cipher,
				Password:   "test-password",
				UDP:        true,
			})
			require.NoError(t, err)
			firstAddress, secondAddress := firstTCP.Addr().String(), secondTCP.Addr().String()
			require.NoError(t, firstTCP.Close())
			require.NoError(t, firstUDP.Close())
			require.NoError(t, secondUDP.Close())

			requireBindConflict(t, named.Listen(nil), "tcp", secondAddress)
			assert.NotPanics(t, func() { assert.Empty(t, named.Address()) })
			assertBindAvailable(t, "tcp4", firstAddress)
			assertBindAvailable(t, "udp4", firstAddress)
			assertBindAvailable(t, "udp4", secondAddress)
			require.NoError(t, secondTCP.Close())
			require.NoError(t, named.Listen(nil))
			t.Cleanup(func() { _ = named.Close() })
			assertBindOccupied(t, firstAddress, secondAddress)
		})
	}
}

func reservePartialBindAddress(t *testing.T) (*net.TCPListener, *net.UDPConn) {
	t.Helper()
	for {
		tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { _ = tcp.Close() })
		addr := tcp.Addr().(*net.TCPAddr)
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: addr.IP, Port: addr.Port})
		if isPartialBindConflict(err) {
			_ = tcp.Close()
			continue
		}
		require.NoError(t, err)
		t.Cleanup(func() { _ = udp.Close() })
		return tcp, udp
	}
}

func partialBindBaseOption(name string, first, second *net.TCPListener) inbound.BaseOption {
	return inbound.BaseOption{
		NameStr: name,
		Listen:  "127.0.0.1",
		Port:    fmt.Sprintf("%d,%d", first.Addr().(*net.TCPAddr).Port, second.Addr().(*net.TCPAddr).Port),
	}
}

func requireBindConflict(t *testing.T, err error, network, address string) {
	t.Helper()
	require.True(t, isPartialBindConflict(err), "expected a bind conflict, got %v", err)
	var opErr *net.OpError
	require.ErrorAs(t, err, &opErr)
	require.Equal(t, network, opErr.Net)
	require.Equal(t, address, opErr.Addr.String())
}

func isPartialBindConflict(err error) bool {
	if runtime.GOOS == "windows" {
		var syscallErr *os.SyscallError
		return errors.As(err, &syscallErr) && syscallErr.Syscall == "bind"
	}
	return errors.Is(err, syscall.EADDRINUSE)
}

func assertBindAvailable(t *testing.T, network, address string) {
	t.Helper()
	if network == "tcp4" {
		l, err := net.Listen(network, address)
		if assert.NoError(t, err, "%s should be released", address) {
			assert.NoError(t, l.Close())
		}
		return
	}
	l, err := net.ListenPacket(network, address)
	if assert.NoError(t, err, "%s should be released", address) {
		assert.NoError(t, l.Close())
	}
}

func assertBindOccupied(t *testing.T, addresses ...string) {
	t.Helper()
	for _, address := range addresses {
		tcp, err := net.Listen("tcp4", address)
		if err == nil {
			_ = tcp.Close()
		}
		requireBindConflict(t, err, "tcp4", address)
		udp, err := net.ListenPacket("udp4", address)
		if err == nil {
			_ = udp.Close()
		}
		requireBindConflict(t, err, "udp4", address)
	}
}
