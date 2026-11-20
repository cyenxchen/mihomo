//go:build !android || !cmfa

package hub

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"

	"github.com/stretchr/testify/require"
)

const forkTestDeviceBlock = "device-overrides:\n  - when: {device-name: reload-test}\n    prepend: {rules: ['DOMAIN,device.invalid,REJECT']}\n"

func forkReloadAddresses(t *testing.T, count int) []*net.TCPAddr {
	t.Helper()
	var addresses []*net.TCPAddr
	for len(addresses) < count {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		addr := l.Addr().(*net.TCPAddr)
		udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: addr.IP, Port: addr.Port})
		if errors.Is(err, syscall.EADDRINUSE) {
			l.Close()
			continue
		}
		defer l.Close()
		require.NoError(t, err)
		defer udp.Close()
		addresses = append(addresses, addr)
	}
	return addresses
}

func forkReloadDial(addr *net.TCPAddr) error {
	c, err := net.DialTimeout("tcp4", addr.String(), time.Second)
	if err == nil {
		c.Close()
	}
	return err
}

func forkReloadRequest(t *testing.T, client *http.Client, method, url, secret, payload string, want int) net.Conn {
	t.Helper()
	body, err := json.Marshal(map[string]string{"payload": payload})
	require.NoError(t, err)
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	var conn net.Conn
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { conn = info.Conn }}
	resp, err := client.Do(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	require.NoError(t, err)
	defer resp.Body.Close()
	result, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equalf(t, want, resp.StatusCode, "%s %s: %s", method, url, result)
	return conn
}

func forkReloadHome(t *testing.T) {
	t.Helper()
	t.Setenv("MIHOMO_DEVICE_NAME", "reload-test")
	oldHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(oldHome) })
	t.Cleanup(func() { require.NoError(t, Parse([]byte("profile: {store-selected: false}\n"))) })
}

func TestDeviceReloadControllerSwap(t *testing.T) {
	forkReloadHome(t)
	addr := forkReloadAddresses(t, 2)
	profile := func(api, proxy *net.TCPAddr) string {
		return fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\nmixed-port: %d\n", api, proxy.Port)
	}
	require.NoError(t, Parse([]byte(profile(addr[0], addr[1]))))
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	swap := profile(addr[1], addr[0]) + forkTestDeviceBlock
	forkReloadRequest(t, client, "PUT", "http://"+addr[0].String()+"/configs", "", swap, 204)
	forkReloadRequest(t, client, "GET", "http://"+addr[1].String()+"/", "", "", 200)
	c, err := net.DialTimeout("tcp4", addr[0].String(), time.Second)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetDeadline(time.Now().Add(time.Second)))
	_, err = c.Write([]byte{5, 1, 0})
	require.NoError(t, err)
	var greeting [2]byte
	_, err = io.ReadFull(c, greeting[:])
	require.NoError(t, err)
	require.Equal(t, [2]byte{5, 0}, greeting)
}

func TestDeviceReloadRemovesUI(t *testing.T) {
	forkReloadHome(t)
	addr := forkReloadAddresses(t, 1)[0]
	ui := filepath.Join(C.Path.HomeDir(), "ui")
	require.NoError(t, os.Mkdir(ui, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(ui, "test.txt"), []byte("device UI"), 0o600))
	base := fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\n", addr)
	require.NoError(t, Parse([]byte(base+"external-ui: ui\n"+forkTestDeviceBlock)))
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	url := "http://" + addr.String()
	forkReloadRequest(t, client, "GET", url+"/ui/test.txt", "", "", 200)
	forkReloadRequest(t, client, "PUT", url+"/configs", "", base, 204)
	forkReloadRequest(t, client, "GET", url+"/ui/test.txt", "", "", 404)
}

func TestDeviceReloadRetiresWebSocket(t *testing.T) {
	forkReloadHome(t)
	addr := forkReloadAddresses(t, 1)[0]
	base := fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\n", addr)
	require.NoError(t, Parse([]byte(base+"secret: old\n"+forkTestDeviceBlock)))
	c, err := net.DialTimeout("tcp4", addr.String(), time.Second)
	require.NoError(t, err)
	defer c.Close()
	require.NoError(t, c.SetDeadline(time.Now().Add(3*time.Second)))
	_, err = fmt.Fprintf(c, "GET /logs?level=error HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nAuthorization: Bearer old\r\n\r\n", addr)
	require.NoError(t, err)
	reader := bufio.NewReader(c)
	resp, err := http.ReadResponse(reader, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	forkReloadRequest(t, client, "PUT", "http://"+addr.String()+"/configs", "old", base+"secret: new\n", 204)
	_, err = reader.ReadByte()
	require.ErrorIs(t, err, io.EOF, "the authenticated subscription should end on retirement")
}

func TestDeviceReloadHTTPSBindRetry(t *testing.T) {
	forkReloadHome(t)
	fixture := httptest.NewUnstartedServer(nil)
	fixture.EnableHTTP2 = true
	fixture.StartTLS()
	defer fixture.Close()
	cert := fixture.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(t, err)
	for name, block := range map[string]*pem.Block{
		"server.pem": {Type: "CERTIFICATE", Bytes: cert.Certificate[0]},
		"server.key": {Type: "PRIVATE KEY", Bytes: key},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(C.Path.HomeDir(), name), pem.EncodeToMemory(block), 0o600))
	}
	client := fixture.Client()
	client.Timeout = 5 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	addrs := forkReloadAddresses(t, 3)
	pinned, ignored, firstTLS := addrs[0], addrs[1], addrs[2]
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	targetTLS := occupied.Addr().String()
	base := fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\ntls: {certificate: server.pem, private-key: server.key}\n", ignored)
	options := []Option{WithExternalController(pinned.String())}
	require.NoError(t, Parse([]byte(base), options...))
	options[0] = WithExternalController(ignored.String())
	httpURL := "http://" + pinned.String() + "/configs"
	device := base + "external-controller-tls: " + firstTLS.String() + "\n" + forkTestDeviceBlock
	forkReloadRequest(t, client, "PUT", httpURL, "", device, 204)
	conn := forkReloadRequest(t, client, "GET", "https://"+firstTLS.String()+"/", "", "", 200)
	require.Equal(t, "h2", conn.(*tls.Conn).ConnectionState().NegotiatedProtocol)
	require.Error(t, forkReloadDial(ignored))

	plain := base + "external-controller-tls: " + targetTLS + "\n"
	forkReloadRequest(t, client, "PUT", "https://"+firstTLS.String()+"/configs", "", plain, 500)
	require.Error(t, forkReloadDial(firstTLS))
	require.NoError(t, occupied.Close())
	forkReloadRequest(t, client, "PUT", httpURL, "", plain, 204)
	forkReloadRequest(t, client, "GET", "https://"+targetTLS+"/", "", "", 200)
	require.Error(t, forkReloadDial(ignored))
}

func TestDeviceFullReloadReadsClientCA(t *testing.T) {
	forkReloadHome(t)
	cert, _, _, err := ca.NewRandomTLSKeyPair(ca.KeyPairTypeP256)
	require.NoError(t, err)
	file := filepath.Join(C.Path.HomeDir(), "client-ca.pem")
	require.NoError(t, os.WriteFile(file, []byte(cert), 0o600))
	addr := forkReloadAddresses(t, 1)[0]
	profile := []byte(fmt.Sprintf("profile: {store-selected: false}\nexternal-controller-tls: %s\ntls: {client-auth-cert: client-ca.pem}\n", addr) + forkTestDeviceBlock)
	require.NoError(t, Parse(profile))
	require.NoError(t, forkReloadDial(addr))
	require.NoError(t, os.Remove(file))
	require.NoError(t, Parse(profile))
	require.Error(t, forkReloadDial(addr), "a full reload should release the controller that trusted the old CA")
}

func TestDeviceReloadFollowedByFullReload(t *testing.T) {
	forkReloadHome(t)
	endpoints := forkReloadAddresses(t, 3)
	profile := func(addr *net.TCPAddr) string {
		return fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\n", addr)
	}
	require.NoError(t, Parse([]byte(profile(endpoints[0]))))
	started, release := make(chan struct{}), make(chan struct{})
	executor.SetApplyConfigHook(func(cfg *config.Config) {
		if cfg.HasDeviceOverrides {
			close(started)
			<-release
		}
	})
	t.Cleanup(func() { executor.SetApplyConfigHook(nil) })
	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	putDone := make(chan struct{})
	go func() {
		defer close(putDone)
		forkReloadRequest(t, client, "PUT", "http://"+endpoints[0].String()+"/configs", "",
			profile(endpoints[1])+forkTestDeviceBlock, 204)
	}()
	select {
	case <-started:
	case <-putDone:
		t.Fatal("PUT returned before applying the device profile")
	}
	fullStarted, fullDone := make(chan struct{}), make(chan error, 1)
	go func() {
		close(fullStarted)
		fullDone <- Parse([]byte(profile(endpoints[2])))
	}()
	<-fullStarted
	close(release)
	<-putDone
	require.NoError(t, <-fullDone)
	forkReloadRequest(t, client, "GET", "http://"+endpoints[2].String()+"/", "", "", 200)
	require.Error(t, forkReloadDial(endpoints[0]))
	require.Error(t, forkReloadDial(endpoints[1]))
}

func TestDeviceOverrideRESTReload(t *testing.T) {
	forkReloadHome(t)
	const base = "profile: {store-selected: false}\n"
	addrs := forkReloadAddresses(t, 4)
	oldAPI, newAPI, oldProxy, newProxy := addrs[0], addrs[1], addrs[2], addrs[3]
	plain := fmt.Sprintf("%sexternal-controller: %s\nmixed-port: %d\n", base, oldAPI, oldProxy.Port)
	overrides := fmt.Sprintf(`device-overrides:
  - when: {device-name: reload-test}
    set:
      allow-lan: true
      bind-address: 127.0.0.1
      mixed-port: %d
      external-controller: %s
      secret: profile-token
`, newProxy.Port, newAPI)
	const token = "startup-token"
	require.NoError(t, Parse([]byte(plain), WithSecret(token)))
	require.Eventually(t, func() bool { return forkReloadDial(oldAPI) == nil }, time.Second, time.Millisecond)

	client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	oldURL, newURL := "http://"+oldAPI.String(), "http://"+newAPI.String()
	forkReloadRequest(t, client, http.MethodPut, oldURL+"/configs", token, plain+overrides, http.StatusNoContent)
	require.NoError(t, forkReloadDial(newAPI), "entering a device profile should bind its controller")
	require.NoError(t, forkReloadDial(newProxy), "entering a device profile should bind its mixed proxy")
	require.Error(t, forkReloadDial(oldAPI))
	require.Error(t, forkReloadDial(oldProxy))
	require.True(t, listener.AllowLan())
	require.Equal(t, "127.0.0.1", listener.BindAddress())
	forkReloadRequest(t, client, http.MethodGet, newURL+"/", "profile-token", "", http.StatusUnauthorized)

	rulesOnly := plain + overrides + "rules: ['MATCH,DIRECT']\n"
	conn := forkReloadRequest(t, client, http.MethodPut, newURL+"/configs", token, rulesOnly, http.StatusNoContent)
	after := forkReloadRequest(t, client, http.MethodGet, newURL+"/", token, "", http.StatusOK)
	require.Same(t, conn, after, "unchanged controller should keep its connection")

	forkReloadRequest(t, client, http.MethodPut, newURL+"/configs", token, plain+"device-overrides: []\n", http.StatusNoContent)
	require.NoError(t, forkReloadDial(oldAPI), "leaving a device profile should restore its base listeners")
	require.NoError(t, forkReloadDial(oldProxy))
	require.Error(t, forkReloadDial(newAPI))
	require.Error(t, forkReloadDial(newProxy))
	require.False(t, listener.AllowLan())

	ordinary := fmt.Sprintf("%sexternal-controller: %s\nmixed-port: %d\n", base, newAPI, newProxy.Port)
	forkReloadRequest(t, client, http.MethodPut, oldURL+"/configs", token, ordinary, http.StatusNoContent)
	require.NoError(t, forkReloadDial(oldProxy), "ordinary reload should preserve the old force behavior")
	require.Error(t, forkReloadDial(newProxy))
	forkReloadRequest(t, client, http.MethodPut, oldURL+"/configs?force=true", token, ordinary, http.StatusNoContent)
	require.NoError(t, forkReloadDial(newProxy))
	require.Error(t, forkReloadDial(oldProxy))
	require.Error(t, forkReloadDial(newAPI), "ordinary reload should leave the controller unchanged")
}

func TestDeviceStartupToleratesUnavailableListener(t *testing.T) {
	forkReloadHome(t)
	busy, err := net.Listen("tcp4", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	control := forkReloadAddresses(t, 1)[0]
	profile := fmt.Sprintf("external-controller: %s\nmixed-port: %d\n", control, busy.Addr().(*net.TCPAddr).Port) + forkTestDeviceBlock
	require.NoError(t, Parse([]byte(profile)))
	client := &http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	forkReloadRequest(t, client, "GET", "http://"+control.String()+"/version", "", "", 200)
	url := "http://" + control.String() + "/configs"
	b, err := json.Marshal(map[string]string{"payload": profile})
	require.NoError(t, err)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(b))
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, 500, resp.StatusCode)
	require.Contains(t, string(body), "applied")
	require.Contains(t, string(body), "mixed")
	require.NoError(t, busy.Close())
	forkReloadRequest(t, client, "PUT", url, "", profile, 204)
	require.Error(t, Parse([]byte("rules: [")))
}

func TestConfigsAvailableWhileProviderLoads(t *testing.T) {
	for _, tc := range []struct{ name, block string }{
		{"ordinary", ""}, {"device", forkTestDeviceBlock},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forkReloadHome(t)
			addrs := forkReloadAddresses(t, 4)
			profile := func(index int) string {
				return fmt.Sprintf("profile: {store-selected: false}\nexternal-controller: %s\nmixed-port: %d\n", addrs[index], addrs[2+index].Port) + tc.block
			}
			require.NoError(t, Parse([]byte(profile(0))))
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				once.Do(func() { close(started) })
				<-release
				fmt.Fprint(w, "payload: ['+.example.org']\n")
			}))
			defer provider.Close()
			next := profile(1) + fmt.Sprintf("rule-providers:\n  slow: {type: http, behavior: domain, url: %q, path: slow.yaml}\nrules: ['RULE-SET,slow,REJECT']\n", provider.URL)
			done := make(chan struct{})
			var reloadErr error
			go func() {
				defer close(done)
				reloadErr = Parse([]byte(next))
			}()
			defer func() {
				close(release)
				<-done
				require.NoError(t, reloadErr)
			}()
			select {
			case <-started:
			case <-done:
				t.Fatalf("reload returned before loading provider: %v", reloadErr)
			case <-time.After(5 * time.Second):
				t.Fatal("provider was never requested")
			}
			require.Error(t, forkReloadDial(addrs[0]))
			client := &http.Client{Transport: &http.Transport{}, Timeout: time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Get("http://" + addrs[1].String() + "/configs")
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, 200, resp.StatusCode)
			var g struct {
				MixedPort int `json:"mixed-port"`
			}
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&g))
			require.Equal(t, addrs[3].Port, g.MixedPort)
		})
	}
}

func TestDeviceFullReloadKeepsUnchangedHTTP(t *testing.T) {
	forkReloadHome(t)
	addr := forkReloadAddresses(t, 1)[0]
	base := fmt.Sprintf("external-controller: %s\n", addr) + forkTestDeviceBlock
	require.NoError(t, Parse([]byte(base)))
	client := &http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	url := "http://" + addr.String() + "/version"
	before := forkReloadRequest(t, client, "GET", url, "", "", 200)
	require.NoError(t, Parse([]byte(base+"rules: ['MATCH,DIRECT']\n")))
	after := forkReloadRequest(t, client, "GET", url, "", "", 200)
	require.Same(t, before, after, "a rules-only SIGHUP should preserve the HTTP connection")
}
