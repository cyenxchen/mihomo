//go:build (linux && !android) || darwin || freebsd

package hub

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/listener"
	"github.com/metacubex/mihomo/listener/tproxy"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/stretchr/testify/require"
)

func TestDeviceReloadTransparentPorts(t *testing.T) {
	if runtime.GOOS == "linux" {
		probe, err := tproxy.NewUDP("127.0.0.1:0", tunnel.Tunnel)
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skipf("transparent UDP sockets require NET_RAW or NET_ADMIN: %v", err)
		}
		require.NoError(t, err)
		require.NoError(t, probe.Close())
	}

	for _, tc := range []struct {
		field string
		port  func(*listener.Ports) int
	}{
		{"redir-port", func(p *listener.Ports) int { return p.RedirPort }},
		{"tproxy-port", func(p *listener.Ports) int { return p.TProxyPort }},
	} {
		if tc.field == "tproxy-port" && runtime.GOOS != "linux" {
			continue
		}
		t.Run(tc.field, func(t *testing.T) {
			forkReloadHome(t)
			addrs := forkReloadAddresses(t, 3)
			a, b, c := addrs[0], addrs[1], addrs[2]
			profile := func(api *net.TCPAddr, transparent, mixed int) string {
				return fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\n%s: %d\nmixed-port: %d\n", api, tc.field, transparent, mixed) + forkTestDeviceBlock
			}
			client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			url := func(api *net.TCPAddr) string { return "http://" + api.String() + "/configs" }

			t.Run("rotation", func(t *testing.T) {
				require.NoError(t, Parse([]byte(profile(a, b.Port, c.Port))))
				require.Equal(t, b.Port, tc.port(listener.GetPorts()))
				forkReloadRequest(t, client, "PUT", url(a), "", profile(b, c.Port, a.Port), 204)
				require.Equal(t, c.Port, tc.port(listener.GetPorts()), "transparent port after rotation")
				require.Equal(t, a.Port, listener.GetPorts().MixedPort)
			})

			t.Run("occupied TCP", func(t *testing.T) {
				require.NoError(t, Parse([]byte(profile(a, 0, 0))))
				busy, err := net.ListenTCP("tcp4", b)
				require.NoError(t, err)
				defer busy.Close()
				forkReloadRequest(t, client, "PUT", url(a), "", profile(a, b.Port, 0), 500)
				require.Zero(t, tc.port(listener.GetPorts()))
				require.NoError(t, busy.Close())
				forkReloadRequest(t, client, "PUT", url(a), "", profile(a, b.Port, 0), 204)
				require.Equal(t, b.Port, tc.port(listener.GetPorts()))
			})

			if runtime.GOOS == "linux" {
				t.Run("occupied UDP", func(t *testing.T) {
					require.NoError(t, Parse([]byte(profile(a, 0, 0))))
					addr := &net.UDPAddr{IP: b.IP, Port: b.Port}
					busy, err := net.ListenUDP("udp4", addr)
					require.NoError(t, err)
					defer busy.Close()
					forkReloadRequest(t, client, "PUT", url(a), "", profile(a, b.Port, 0), 500)
					require.Equal(t, b.Port, tc.port(listener.GetPorts()), "TCP remains usable")
					require.NoError(t, busy.Close())
					forkReloadRequest(t, client, "PUT", url(a), "", profile(a, b.Port, 0), 204)
					free, err := net.ListenUDP("udp4", addr)
					if free != nil {
						free.Close()
					}
					require.ErrorIs(t, err, syscall.EADDRINUSE, "retry must reacquire UDP")
				})
			}
		})
	}
}

func TestUnprivilegedRedirKeepsOrdinaryReloadSemantics(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires Linux without transparent socket privileges")
	}
	probe, err := tproxy.NewUDP("127.0.0.1:0", tunnel.Tunnel)
	if err == nil {
		require.NoError(t, probe.Close())
		t.Skip("requires a process without NET_RAW and NET_ADMIN")
	}
	require.ErrorIs(t, err, os.ErrPermission)

	forkReloadHome(t)
	addrs := forkReloadAddresses(t, 3)
	api, redir, mixed := addrs[0], addrs[1], addrs[2]
	profile := fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\nredir-port: %d\n", api, redir.Port)
	device := profile + forkTestDeviceBlock
	require.NoError(t, Parse([]byte(device)))
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	url := "http://" + api.String() + "/configs"
	busy, err := net.ListenUDP("udp4", &net.UDPAddr{IP: redir.IP, Port: redir.Port})
	require.NoError(t, err)
	defer busy.Close()
	forkReloadRequest(t, client, "PUT", url, "", device, 500)
	require.NoError(t, busy.Close())
	forkReloadRequest(t, client, "PUT", url, "", device, 204)
	require.NoError(t, forkReloadDial(redir))
	forkReloadRequest(t, client, "PUT", url, "", profile, 204)
	next := profile + fmt.Sprintf("mixed-port: %d\n", mixed.Port)
	forkReloadRequest(t, client, "PUT", url, "", next, 204)
	require.Zero(t, listener.GetPorts().MixedPort, "ordinary PUT must recover its original force behavior")
	forkReloadRequest(t, client, "PUT", url+"?force=true", "", next, 204)
	require.NoError(t, forkReloadDial(mixed))

	sub := log.Subscribe()
	defer log.UnSubscribe(sub)
	swapped := fmt.Sprintf("external-controller: %s\nredir-port: %d\n", api, mixed.Port) + forkTestDeviceBlock
	forkReloadRequest(t, client, "PUT", url, "", swapped, 204)
	require.NoError(t, forkReloadDial(mixed))
	marker := t.Name() + " complete"
	log.Debugln("%s", marker)
	var warnings int
	for event := range sub {
		if event.Payload == marker {
			break
		}
		if event.LogLevel == log.WARNING && strings.Contains(event.Payload, "without transparent UDP") {
			warnings++
		}
	}
	require.Equal(t, 1, warnings, "report TCP-only operation when the retry takes the retiring mixed port")
}
