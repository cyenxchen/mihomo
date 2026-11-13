package route

import (
	"context"
	"encoding/json"
	"io"
	"net"

	"github.com/metacubex/mihomo/component/netenv"
	"github.com/metacubex/mihomo/hub/executor"

	"github.com/metacubex/chi"
	"github.com/metacubex/chi/render"
	"github.com/metacubex/http"
)

type forkNetworkAuthKey struct{}

func forkNetworkAuthentication(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authenticated := secret != ""
			if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
				authenticated = authenticated || addr.Network() == "unix" || addr.Network() == "pipe"
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), forkNetworkAuthKey{}, authenticated)))
		})
	}
}

func init() {
	Register(func(r chi.Router) {
		r.Get("/network", forkGetNetwork)
		r.Put("/network", forkPutNetwork)
		r.Delete("/network", forkDeleteNetwork)
	})
}

func forkGetNetwork(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, struct {
		netenv.View
		Groups []map[string]any `json:"groups"`
	}{netenv.Default.View(), executor.ForkSubnetGroups()})
}

func forkNetworkWritable(w http.ResponseWriter, r *http.Request) bool {
	authenticated, _ := r.Context().Value(forkNetworkAuthKey{}).(bool)
	if embedMode || !authenticated {
		http.Error(w, "network writes are forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func forkPutNetwork(w http.ResponseWriter, r *http.Request) {
	if !forkNetworkWritable(w, r) {
		return
	}
	raw := map[string]any{}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	if err := decoder.Decode(&raw); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if raw == nil {
		http.Error(w, "network report must be an object", 400)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "unexpected trailing JSON", 400)
		return
	}
	fields, err := netenv.ParseFields(raw, true)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := netenv.Default.Report(r.Context(), fields, "rest"); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func forkDeleteNetwork(w http.ResponseWriter, r *http.Request) {
	if !forkNetworkWritable(w, r) {
		return
	}
	netenv.Default.ClearReport()
	w.WriteHeader(http.StatusNoContent)
}
