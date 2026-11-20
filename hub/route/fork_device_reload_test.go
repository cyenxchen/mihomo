package route

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel"

	"github.com/metacubex/http"
	"github.com/metacubex/http/httptest"
	"github.com/stretchr/testify/require"
)

type forkFailedUpgradeWriter struct {
	*httptest.ResponseRecorder
	conn net.Conn
}

func (w *forkFailedUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

func TestFailedWebSocketHandshakeReleasesControllerConnection(t *testing.T) {
	local, peer := net.Pipe()
	defer local.Close()
	require.NoError(t, peer.Close())
	listener := &forkControllerListener{conns: make(map[*forkControllerConn]struct{})}
	conn := &forkControllerConn{Conn: local, listener: listener}
	listener.conns[conn] = struct{}{}
	req := httptest.NewRequest("GET", "/logs", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	_, _, err := wsUpgrade(req, &forkFailedUpgradeWriter{httptest.NewRecorder(), conn})
	require.Error(t, err)
	require.Empty(t, listener.conns, "failed handshakes must release hijacked sockets")
}

func TestEmbeddedConfigRouterRejectsRESTReload(t *testing.T) {
	old := embedMode
	SetEmbedMode(true)
	t.Cleanup(func() { SetEmbedMode(old) })
	recorder := httptest.NewRecorder()
	configRouter().ServeHTTP(recorder, httptest.NewRequest("PUT", "/", nil))
	require.Equal(t, http.StatusMethodNotAllowed, recorder.Code)
}

func TestRetiredControllerPendingReload(t *testing.T) {
	t.Setenv("MIHOMO_DEVICE_NAME", "reload-test")
	oldHome := C.Path.HomeDir()
	C.SetHomeDir(t.TempDir())
	t.Cleanup(func() { C.SetHomeDir(oldHome) })
	const base = "profile: {store-selected: false}\n"
	t.Cleanup(func() { require.NoError(t, ParseConfig([]byte(base), nil)) })
	profile := base + "external-controller: 127.0.0.1:0\ndevice-overrides:\n  - when: {device-name: reload-test}\n    set: {allow-lan: false}\n"
	require.NoError(t, ParseConfig([]byte(profile+"secret: old\n"), nil))
	oldServer := httpServer.server
	require.NoError(t, ParseConfig([]byte(profile+"secret: new\n"), nil))
	for _, tc := range []struct {
		method, payload string
		handler         http.HandlerFunc
	}{
		{"PUT", `{"payload":"mode: direct\nprofile: {store-selected: false}"}`, updateConfigs},
		{"PATCH", `{"mode":"direct"}`, patchConfigs},
		{"GET", "", getConfigs},
	} {
		t.Run(tc.method, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/configs", strings.NewReader(tc.payload))
			req = req.WithContext(context.WithValue(oldServer.BaseContext(nil), http.ServerContextKey, oldServer))
			recorder := httptest.NewRecorder()
			tc.handler(recorder, req)
			require.Equal(t, http.StatusConflict, recorder.Code)
			require.Equal(t, tunnel.Rule, tunnel.Mode())
		})
	}
}
