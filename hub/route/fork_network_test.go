package route

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/netenv"

	"github.com/metacubex/http"
	"github.com/metacubex/http/httptest"
)

type forkLocalAddr string

func (a forkLocalAddr) Network() string { return string(a) }
func (a forkLocalAddr) String() string  { return "local" }
func TestForkNetworkREST(t *testing.T) {
	oldStore, oldEmbed := netenv.Default, embedMode
	defer func() { netenv.Default = oldStore; embedMode = oldEmbed }()
	netenv.Default = netenv.NewStore(time.Now)
	for _, tc := range []struct {
		name, method, secret, auth, network, body string
		embed                                     bool
		want                                      int
	}{
		{"read", "GET", "", "", "tcp", "", false, 200},
		{"no secret put", "PUT", "", "", "tcp", `{"ssid":"GOLDWIND"}`, false, 403},
		{"no secret delete", "DELETE", "", "", "tcp", "", false, 403},
		{"unauthenticated", "PUT", "secret", "", "tcp", `{}`, false, 401},
		{"authenticated", "PUT", "secret", "Bearer secret", "tcp", `{"type":"wifi","ssid":"GOLDWIND"}`, false, 204},
		{"unix", "PUT", "", "", "unix", `{"type":"none"}`, false, 204},
		{"pipe", "PUT", "", "", "pipe", `{"type":"wifi"}`, false, 204},
		{"embedded http", "PUT", "secret", "Bearer secret", "tcp", `{}`, true, 403},
		{"embedded local", "DELETE", "", "", "unix", "", true, 403},
		{"bad field", "PUT", "secret", "Bearer secret", "tcp", `{"sssid":"x"}`, false, 400},
		{"bad mac", "PUT", "secret", "Bearer secret", "tcp", `{"bssid":"x"}`, false, 400},
		{"null missing", "PUT", "secret", "Bearer secret", "tcp", `{"ssid":null}`, false, 204},
		{"delete", "DELETE", "secret", "Bearer secret", "tcp", "", false, 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			embedMode = tc.embed
			r := router(false, tc.secret, "", Cors{})
			req := httptest.NewRequest(tc.method, "/network", strings.NewReader(tc.body))
			req.Header.Set("Authorization", tc.auth)
			req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, forkLocalAddr(tc.network)))
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}
