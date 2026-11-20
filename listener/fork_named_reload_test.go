package listener_test

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/stretchr/testify/require"
)

func TestDeviceReloadNamedListeners(t *testing.T) {
	t.Run("takes a retiring mixed port", func(t *testing.T) {
		port, reservation := reserveNamedListenerPort(t)
		require.NoError(t, reservation.Close())
		t.Cleanup(func() { listener.ReCreateMixed(0, nil) })
		t.Cleanup(func() { listener.PatchInboundListeners(nil, nil, true) })
		listener.ReCreateMixed(port, tunnel.Tunnel)
		require.Equal(t, port, listener.GetPorts().MixedPort)

		named := parseNamedHTTP(t, "device-http", port)
		cfg := &config.Config{
			General: &config.General{},
			DNS:     &config.DNS{},
			Listeners: map[string]C.InboundListener{
				named.Name(): named,
			},
		}
		listener.PatchInboundListeners(cfg.Listeners, tunnel.Tunnel, true)
		listener.ReCreateMixed(0, nil)

		require.NoError(t, executor.ForkCheckDeviceListeners(cfg))
		requireNamedListenerBound(t, named)
	})

	t.Run("swaps named ports", func(t *testing.T) {
		firstPort, first := reserveNamedListenerPort(t)
		secondPort, second := reserveNamedListenerPort(t)
		require.NoError(t, first.Close())
		require.NoError(t, second.Close())
		t.Cleanup(func() { listener.PatchInboundListeners(nil, nil, true) })
		listener.PatchInboundListeners(map[string]C.InboundListener{
			"first":  parseNamedHTTP(t, "first", firstPort),
			"second": parseNamedHTTP(t, "second", secondPort),
		}, tunnel.Tunnel, true)

		firstNamed := parseNamedHTTP(t, "first", secondPort)
		secondNamed := parseNamedHTTP(t, "second", firstPort)
		listener.PatchInboundListeners(map[string]C.InboundListener{
			"first":  firstNamed,
			"second": secondNamed,
		}, tunnel.Tunnel, true)

		requireNamedListenerBound(t, firstNamed)
		requireNamedListenerBound(t, secondNamed)
	})

	t.Run("reports a named port conflict", func(t *testing.T) {
		port, reservation := reserveNamedListenerPort(t)
		t.Cleanup(func() { listener.PatchInboundListeners(nil, nil, true) })
		named := parseNamedHTTP(t, "occupied-http", port)
		cfg := &config.Config{
			General: &config.General{},
			DNS:     &config.DNS{},
			Listeners: map[string]C.InboundListener{
				named.Name(): named,
			},
		}

		require.ErrorContains(t, executor.ForkCheckDeviceListeners(cfg), named.Name())
		require.NoError(t, reservation.Close())
		require.NoError(t, executor.ForkCheckDeviceListeners(cfg))
		requireNamedListenerBound(t, named)
	})

	t.Run("rebinds a retired configuration after a failed replacement", func(t *testing.T) {
		oldPort, oldReservation := reserveNamedListenerPort(t)
		newPort, _ := reserveNamedListenerPort(t)
		require.NoError(t, oldReservation.Close())
		t.Cleanup(func() { listener.PatchInboundListeners(nil, nil, true) })
		const name = "device-http"
		listener.PatchInboundListeners(map[string]C.InboundListener{
			name: parseNamedHTTP(t, name, oldPort),
		}, tunnel.Tunnel, true)
		listener.PatchInboundListeners(map[string]C.InboundListener{
			name: parseNamedHTTP(t, name, newPort),
		}, tunnel.Tunnel, true)

		restored := parseNamedHTTP(t, name, oldPort)
		listener.PatchInboundListeners(map[string]C.InboundListener{name: restored}, tunnel.Tunnel, true)
		requireNamedListenerBound(t, restored)
	})

	t.Run("rolls back a partially bound HTTP port list", func(t *testing.T) {
		firstPort, first := reserveNamedListenerPort(t)
		secondPort, second := reserveNamedListenerPort(t)
		require.NoError(t, first.Close())
		named := parseNamedListener(t, "http", "multi-port-http", fmt.Sprintf("%d,%d", firstPort, secondPort))
		t.Cleanup(func() { _ = named.Close() })
		require.Error(t, named.Listen(tunnel.Tunnel))
		require.Empty(t, named.Address())
		firstAgain, err := net.Listen("tcp4", first.Addr().String())
		require.NoError(t, err)
		require.NoError(t, firstAgain.Close())
		require.NoError(t, second.Close())
		require.NoError(t, named.Listen(tunnel.Tunnel))
		requireNamedListenerBound(t, named)
	})

	for _, proxyType := range []string{"socks", "mixed"} {
		t.Run("rolls back "+proxyType+" TCP on UDP conflict", func(t *testing.T) {
			reservation, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			require.NoError(t, err)
			t.Cleanup(func() { _ = reservation.Close() })
			port := reservation.LocalAddr().(*net.UDPAddr).Port
			named := parseNamedListener(t, proxyType, "device-"+proxyType, strconv.Itoa(port))
			t.Cleanup(func() { _ = named.Close() })
			require.Error(t, named.Listen(tunnel.Tunnel))
			require.Empty(t, named.Address())
			tcp, err := net.Listen("tcp4", named.RawAddress())
			require.NoError(t, err)
			require.NoError(t, tcp.Close())
			require.NoError(t, reservation.Close())
			require.NoError(t, named.Listen(tunnel.Tunnel))
			requireNamedListenerBound(t, named)
		})
	}
}

func TestNamedListenerRejectsMalformedTLSWithoutBinding(t *testing.T) {
	for _, proxyType := range []string{"http", "socks", "mixed"} {
		t.Run(proxyType, func(t *testing.T) {
			port, reservation := reserveNamedListenerPort(t)
			require.NoError(t, reservation.Close())
			named, err := listener.ParseListener(map[string]any{
				"name":        "invalid-tls-" + proxyType,
				"type":        proxyType,
				"listen":      "127.0.0.1",
				"port":        port,
				"certificate": "-----BEGIN CERTIFICATE-----\ninvalid\n-----END CERTIFICATE-----",
				"private-key": "-----BEGIN PRIVATE KEY-----\ninvalid\n-----END PRIVATE KEY-----",
			})
			require.NoError(t, err)
			require.ErrorContains(t, named.Listen(tunnel.Tunnel), "parse certificate failed")
			socket, err := net.Listen("tcp4", named.RawAddress())
			require.NoError(t, err)
			require.NoError(t, socket.Close())
		})
	}
}

func reserveNamedListenerPort(t *testing.T) (int, net.Listener) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port, l
}

func parseNamedHTTP(t *testing.T, name string, port int) C.InboundListener {
	t.Helper()
	return parseNamedListener(t, "http", name, strconv.Itoa(port))
}

func parseNamedListener(t *testing.T, proxyType, name, ports string) C.InboundListener {
	t.Helper()
	named, err := listener.ParseListener(map[string]any{
		"name":   name,
		"type":   proxyType,
		"listen": "127.0.0.1",
		"port":   ports,
	})
	require.NoError(t, err)
	return named
}

func requireNamedListenerBound(t *testing.T, named C.InboundListener) {
	t.Helper()
	require.Equal(t, named.RawAddress(), named.Address(), "named listener %s", named.Name())
	for _, address := range strings.Split(named.Address(), ",") {
		conn, err := net.DialTimeout("tcp4", address, time.Second)
		require.NoError(t, err)
		_ = conn.Close()
	}
}
