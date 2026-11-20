//go:build !android || !cmfa

package hub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	IN "github.com/metacubex/mihomo/listener/inbound"

	D "github.com/miekg/dns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestDeviceOverrideExampleMTLS(t *testing.T) {
	source, err := os.ReadFile("../docs/device-overrides.yaml")
	require.NoError(t, err)

	t.Run("non_gateway", func(t *testing.T) {
		forkReloadHome(t)
		addrs := forkReloadAddresses(t, 3)
		proxy, api, dns := addrs[0], addrs[1], addrs[2]
		profile := forkLocalDeviceExample(t, source, "another-gateway", proxy, api, dns)
		require.NoError(t, Parse(profile))
		status, err := forkExampleGET(t, "http://"+api.String()+"/", nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		greeting, err := forkExampleSOCKS(proxy, nil)
		require.NoError(t, err)
		require.Equal(t, [2]byte{5, 0}, greeting)
		forkExampleDNS(t, dns)
	})

	t.Run("missing_certificates", func(t *testing.T) {
		forkReloadHome(t)
		addrs := forkReloadAddresses(t, 3)
		proxy, api, dns := addrs[0], addrs[1], addrs[2]
		profile := forkLocalDeviceExample(t, source, "example-home-gateway", proxy, api, dns)
		_ = Parse(profile)
		assert.Error(t, forkReloadDial(proxy), "gateway must not open the proxy without its certificates")
		assert.Error(t, forkReloadDial(api), "gateway must not open the controller without its certificates")
		forkExampleDNS(t, dns)
	})

	t.Run("trusted_clients_only", func(t *testing.T) {
		forkReloadHome(t)
		addrs := forkReloadAddresses(t, 3)
		proxy, api, dns := addrs[0], addrs[1], addrs[2]
		profile := forkLocalDeviceExample(t, source, "example-home-gateway", proxy, api, dns)
		trusted, untrusted, _ := forkExampleCredentials(t)
		require.NoError(t, Parse(profile))

		status, err := forkExampleGET(t, "http://"+api.String()+"/", nil)
		if err == nil {
			require.NotEqual(t, http.StatusOK, status, "the gateway must not serve the API over plaintext")
		}
		_, err = forkExampleSOCKS(proxy, nil)
		require.Error(t, err, "the gateway must not serve SOCKS over plaintext")

		for _, tc := range []struct {
			name       string
			clientCert *tls.Certificate
			wantAccess bool
		}{
			{name: "no_client_certificate"},
			{name: "untrusted_client_certificate", clientCert: &untrusted},
			{name: "trusted_client_certificate", clientCert: &trusted, wantAccess: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				roots := x509.NewCertPool()
				issuer, err := os.ReadFile(filepath.Join(C.Path.HomeDir(), "gateway-tls/client-ca.pem"))
				require.NoError(t, err)
				require.True(t, roots.AppendCertsFromPEM(issuer))
				cfg := &tls.Config{
					RootCAs:    roots,
					ServerName: proxy.IP.String(),
					GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
						if tc.clientCert != nil {
							return tc.clientCert, nil
						}
						return &tls.Certificate{}, nil
					},
				}
				status, apiErr := forkExampleGET(t, "https://"+api.String()+"/", cfg)
				greeting, proxyErr := forkExampleSOCKS(proxy, cfg)
				if tc.wantAccess {
					require.NoError(t, apiErr)
					require.Equal(t, http.StatusOK, status)
					require.NoError(t, proxyErr)
					require.Equal(t, [2]byte{5, 0}, greeting)
					return
				}
				require.Error(t, apiErr)
				require.Error(t, proxyErr)
			})
		}

		packet, err := net.ListenPacket("udp4", proxy.String())
		require.NoError(t, err, "the gateway must not listen for SOCKS UDP")
		require.NoError(t, packet.Close())
		forkExampleDNS(t, dns)
	})
}

type forkBindCounter struct {
	C.InboundListenConfig `json:"-"`
	tcp                   int
}

func (c *forkBindCounter) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	c.tcp++
	return c.InboundListenConfig.Listen(ctx, network, address)
}

func TestFullReloadRotatesNamedClientCA(t *testing.T) {
	for _, tc := range []struct {
		name   string
		device bool
	}{{"ordinary", false}, {"device", true}} {
		t.Run(tc.name, func(t *testing.T) {
			forkReloadHome(t)
			oldClient, newClient, newCA := forkExampleCredentials(t)
			caFile := filepath.Join(C.Path.HomeDir(), "gateway-tls/client-ca.pem")
			oldCA, err := os.ReadFile(caFile)
			require.NoError(t, err)
			roots := x509.NewCertPool()
			require.True(t, roots.AppendCertsFromPEM(oldCA))
			addrs := forkReloadAddresses(t, 5)
			api, mixed, socks, proxyHTTP, plain := addrs[0], addrs[1], addrs[2], addrs[3], addrs[4]
			profile := fmt.Sprintf(`profile: {store-selected: false}
external-controller-tls: %s
tls: &mtls {certificate: gateway-tls/server.pem, private-key: gateway-tls/server.key, client-auth-type: require-and-verify, client-auth-cert: gateway-tls/client-ca.pem}
listeners:
  - {name: mixed, type: mixed, listen: 127.0.0.1, port: %d, udp: false, <<: *mtls}
  - {name: socks, type: socks, listen: 127.0.0.1, port: %d, udp: false, <<: *mtls}
  - {name: http, type: http, listen: 127.0.0.1, port: %d, <<: *mtls}
  - {name: plain, type: http, listen: 127.0.0.1, port: %d}
`, api, mixed.Port, socks.Port, proxyHTTP.Port, plain.Port)
			var options []Option
			var parsed *config.Config
			mixedBinds := &forkBindCounter{InboundListenConfig: new(net.ListenConfig)}
			plainBinds := &forkBindCounter{InboundListenConfig: new(net.ListenConfig)}
			if tc.device {
				profile += forkTestDeviceBlock
				options = []Option{func(c *config.Config) {
					parsed = c
					c.Listeners["mixed"].Config().(*IN.MixedOption).ListenConfigForAPI = mixedBinds
					c.Listeners["plain"].Config().(*IN.HTTPOption).ListenConfigForAPI = plainBinds
				}}
			}
			reload := func() {
				t.Helper()
				require.NoError(t, Parse([]byte(profile), options...))
			}
			tlsConfig := func(cert *tls.Certificate) *tls.Config {
				return &tls.Config{
					RootCAs:    roots,
					ServerName: "127.0.0.1",
					// TLS 1.2 delivers a client certificate rejection before Dial returns.
					MaxVersion:           tls.VersionTLS12,
					GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return cert, nil },
				}
			}
			probe := func(stage string, trusted *tls.Certificate) {
				t.Helper()
				for name, cert := range map[string]*tls.Certificate{"old": &oldClient, "new": &newClient} {
					for endpoint, addr := range map[string]*net.TCPAddr{"controller": api, "mixed": mixed, "socks": socks, "http": proxyHTTP} {
						conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", addr.String(), tlsConfig(cert))
						if conn != nil {
							_ = conn.Close()
						}
						if cert == trusted {
							assert.NoErrorf(t, err, "%s: %s must accept the %s client", stage, endpoint, name)
						} else {
							assert.Errorf(t, err, "%s: %s accepted the %s client", stage, endpoint, name)
						}
					}
				}
				require.NoError(t, forkReloadDial(plain))
			}
			reload()
			probe("initial", &oldClient)
			require.NoError(t, os.WriteFile(caFile, newCA, 0o600))
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig(&oldClient)}, Timeout: 3 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			forkReloadRequest(t, client, "PUT", "https://"+api.String()+"/configs?force=true", "", profile, http.StatusNoContent)
			probe("PUT", &oldClient)
			bindsBefore := mixedBinds.tcp
			reload()
			probe("rotated", &newClient)
			if tc.device {
				require.LessOrEqual(t, mixedBinds.tcp-bindsBefore, 1, "the device retry pass must preserve a working mTLS socket")
			}
			require.NoError(t, os.Remove(caFile))
			reload()
			probe("missing CA", nil)
			require.NoError(t, os.WriteFile(caFile, oldCA, 0o600))
			reload()
			probe("restored", &oldClient)
			require.NoError(t, os.WriteFile(caFile, []byte("invalid CA"), 0o600))
			reload()
			probe("malformed CA", nil)
			require.NoError(t, os.WriteFile(caFile, newCA, 0o600))
			reload()
			probe("repaired", &newClient)
			if tc.device {
				require.NoError(t, os.WriteFile(caFile, oldCA, 0o600))
				ApplyConfig(parsed)
				probe("reapplied parsed config", &oldClient)
				for _, name := range []string{"mixed", "socks", "http"} {
					l := parsed.Listeners[name]
					require.Equal(t, l.RawAddress(), l.Address())
				}
				require.Equal(t, 1, plainBinds.tcp, "the ordinary named HTTP listener must keep its socket")
			}
		})
	}
}

func forkLocalDeviceExample(t *testing.T, source []byte, name string, proxy, api, dns *net.TCPAddr) []byte {
	t.Helper()
	expanded, err := config.ExpandDeviceOverrides(source, "linux", name)
	require.NoError(t, err)
	var profile map[string]any
	require.NoError(t, yaml.Unmarshal(expanded, &profile))
	if port, _ := profile["mixed-port"].(int); port != 0 {
		profile["mixed-port"] = proxy.Port
		profile["bind-address"] = proxy.IP.String()
	}
	if addr, _ := profile["external-controller"].(string); addr != "" {
		profile["external-controller"] = api.String()
	}
	if addr, _ := profile["external-controller-tls"].(string); addr != "" {
		profile["external-controller-tls"] = api.String()
	}
	if listeners, ok := profile["listeners"].([]any); ok {
		for _, listener := range listeners {
			settings := listener.(map[string]any)
			settings["listen"] = proxy.IP.String()
			settings["port"] = proxy.Port
		}
	}
	dnsSettings := profile["dns"].(map[string]any)
	dnsSettings["listen"] = dns.String()
	dnsSettings["nameserver"] = []string{"127.0.0.1:9"}
	profile["hosts"] = map[string]string{"device-example.test": "192.0.2.55"}
	profile["profile"] = map[string]bool{"store-selected": false}
	local, err := yaml.Marshal(map[string]any{
		"device-overrides": []map[string]any{{
			"when": map[string]string{"device-name": "reload-test"},
			"set":  profile,
		}},
	})
	require.NoError(t, err)
	return local
}

func forkExampleGET(t *testing.T, endpoint string, cfg *tls.Config) (int, error) {
	t.Helper()
	transport := &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}
	client := &http.Client{Transport: transport, Timeout: time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func forkExampleSOCKS(address *net.TCPAddr, cfg *tls.Config) (greeting [2]byte, err error) {
	conn, err := net.DialTimeout("tcp4", address.String(), time.Second)
	if err != nil {
		return greeting, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return greeting, err
	}
	if cfg != nil {
		conn = tls.Client(conn, cfg)
	}
	if _, err := conn.Write([]byte{5, 3, 0, 1, 2}); err != nil {
		return greeting, err
	}
	_, err = io.ReadFull(conn, greeting[:])
	return greeting, err
}

func forkExampleDNS(t *testing.T, address *net.TCPAddr) {
	t.Helper()
	query := new(D.Msg).SetQuestion("device-example.test.", D.TypeA)
	for _, network := range []string{"tcp", "udp"} {
		client := D.Client{Net: network, Timeout: time.Second}
		reply, _, err := client.Exchange(query, address.String())
		require.NoError(t, err)
		require.Len(t, reply.Answer, 1)
		answer, ok := reply.Answer[0].(*D.A)
		require.True(t, ok)
		require.Equal(t, "192.0.2.55", answer.A.String())
	}
}

func forkExampleCredentials(t *testing.T) (trusted, untrusted tls.Certificate, otherCA []byte) {
	t.Helper()
	directory := filepath.Join(C.Path.HomeDir(), "gateway-tls")
	require.NoError(t, os.Mkdir(directory, 0o700))
	now := time.Now()
	issue := func(serial int64, name string, usage x509.ExtKeyUsage, issuer *x509.Certificate, issuerKey ed25519.PrivateKey) (*x509.Certificate, ed25519.PrivateKey) {
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		cert := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: name},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
		}
		if issuer == nil {
			cert.BasicConstraintsValid = true
			cert.IsCA = true
			cert.KeyUsage |= x509.KeyUsageCertSign
			issuer, issuerKey = cert, privateKey
		} else {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{usage}
			if usage == x509.ExtKeyUsageServerAuth {
				cert.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
			}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, issuer, publicKey, issuerKey)
		require.NoError(t, err)
		issued, err := x509.ParseCertificate(der)
		require.NoError(t, err)
		return issued, privateKey
	}
	issuer, issuerKey := issue(1, "example test CA", 0, nil, nil)
	otherIssuer, otherIssuerKey := issue(2, "untrusted test CA", 0, nil, nil)
	server, serverKey := issue(3, "example server", x509.ExtKeyUsageServerAuth, issuer, issuerKey)
	validClient, validKey := issue(4, "example client", x509.ExtKeyUsageClientAuth, issuer, issuerKey)
	invalidClient, invalidKey := issue(5, "untrusted client", x509.ExtKeyUsageClientAuth, otherIssuer, otherIssuerKey)
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	require.NoError(t, err)
	for name, block := range map[string]*pem.Block{
		"server.pem":    {Type: "CERTIFICATE", Bytes: server.Raw},
		"server.key":    {Type: "PRIVATE KEY", Bytes: serverKeyDER},
		"client-ca.pem": {Type: "CERTIFICATE", Bytes: issuer.Raw},
	} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(block), 0o600))
	}
	return tls.Certificate{Certificate: [][]byte{validClient.Raw}, PrivateKey: validKey},
		tls.Certificate{Certificate: [][]byte{invalidClient.Raw}, PrivateKey: invalidKey},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: otherIssuer.Raw})
}
