package adapter_test

import (
	"encoding/json"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"
)

type runtimeHealthAdapter struct {
	*outbound.Base
	runtimeAlive bool
}

func (a *runtimeHealthAdapter) RuntimeAlive() bool {
	return a.runtimeAlive
}

func TestProxyMarshalJSONHonorsRuntimeHealth(t *testing.T) {
	underlying := &runtimeHealthAdapter{
		Base: outbound.NewBase(outbound.BaseOption{
			Name: "runtime-health-test",
			Type: C.Tailscale,
		}),
		runtimeAlive: false,
	}
	proxy := adapter.NewProxy(underlying)

	payload, err := proxy.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Alive bool `json:"alive"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatal(err)
	}
	if state.Alive {
		t.Fatal("proxy alive = true while the adapter reports a fatal runtime failure")
	}
}
