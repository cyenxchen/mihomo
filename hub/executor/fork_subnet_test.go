package executor

import (
	"context"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/component/netenv"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

func forkSubnetConfig(t *testing.T, names ...string) *config.Config {
	t.Helper()
	cfg := &config.Config{General: &config.General{}, Proxies: map[string]C.Proxy{"DIRECT": adapter.NewProxy(outbound.NewDirect()), "REJECT": adapter.NewProxy(outbound.NewReject())}}
	for _, name := range names {
		g, err := outboundgroup.ParseProxyGroup(map[string]any{"name": name, "type": "subnet", "default": "REJECT", "matches": []any{map[string]any{"type": "wifi", "proxy": "DIRECT"}}}, cfg.Proxies, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Proxies[name] = adapter.NewProxy(g)
	}
	return cfg
}

func TestForkSubnetCoordinator(t *testing.T) {
	store := netenv.NewStore(time.Now)
	closed := map[string]bool{}
	clears, resets := 0, 0
	c := &forkSubnetCoordinator{store: store, closeConnections: func(names map[string]bool) {
		for k := range names {
			closed[k] = true
		}
	}, invalidate: func() { clears++ }, reset: func() { resets++ }, configureDetection: func(bool, string) {}}
	cancel := store.Subscribe(c.changed)
	defer cancel()
	c.apply(forkSubnetConfig(t, "Corp", "Office"))
	_ = store.Report(context.Background(), netenv.Fields{Type: "none"}, "embed")
	if !closed["Corp"] || !closed["Office"] || len(closed) != 2 || clears != 1 || resets != 1 {
		t.Fatalf("switch effects: %v clear=%d reset=%d", closed, clears, resets)
	}
	c.apply(forkSubnetConfig(t, "Corp", "Office"))
	if clears != 1 {
		t.Fatal("same choice reload invalidated cache")
	}
	_ = store.Report(context.Background(), netenv.Fields{Type: "wifi"}, "embed")
	if clears != 2 || resets != 2 {
		t.Fatal("return to wifi did not switch")
	}
	c.apply(forkSubnetConfig(t))
	_ = store.Report(context.Background(), netenv.Fields{Type: "none"}, "embed")
	if clears != 2 {
		t.Fatal("removed groups retained")
	}
}

func TestForkSubnetReportDoesNotWaitForExecutorLock(t *testing.T) {
	store := netenv.NewStore(time.Now)
	c := &forkSubnetCoordinator{store: store, closeConnections: func(map[string]bool) {}, invalidate: func() {}, reset: func() {}, configureDetection: func(bool, string) {}}
	cancel := store.Subscribe(c.changed)
	defer cancel()
	c.apply(forkSubnetConfig(t, "Corp"))
	mux.Lock()
	defer mux.Unlock()
	if err := store.Report(context.Background(), netenv.Fields{Type: "none"}, "embed"); err != nil {
		t.Fatal(err)
	}
	if c.groups["Corp"] == nil || c.groups["Corp"].Now() != "REJECT" {
		t.Fatal("report not applied while executor locked")
	}
}

type forkSubnetTracker struct {
	C.Connection
	name   string
	chain  C.Chain
	closed bool
}

func (f *forkSubnetTracker) ID() string { return f.name }
func (f *forkSubnetTracker) Info() *statistic.TrackerInfo {
	return &statistic.TrackerInfo{Chain: f.chain}
}
func (f *forkSubnetTracker) Close() error { f.closed = true; return nil }
func TestForkSubnetClosesOnlyMatchingConnections(t *testing.T) {
	a := &forkSubnetTracker{name: "fork-a", chain: C.Chain{"DIRECT", "Corp"}}
	b := &forkSubnetTracker{name: "fork-b", chain: C.Chain{"DIRECT", "Other"}}
	statistic.DefaultManager.Join(a)
	statistic.DefaultManager.Join(b)
	defer statistic.DefaultManager.Leave(a)
	defer statistic.DefaultManager.Leave(b)
	forkCloseSubnetConnections(map[string]bool{"Corp": true})
	if !a.closed || b.closed {
		t.Fatalf("wrong connections closed: %t %t", a.closed, b.closed)
	}
}

func TestForkSubnetReloadChangesSelectionInOrder(t *testing.T) {
	store := netenv.NewStore(time.Now)
	_ = store.Report(context.Background(), netenv.Fields{Type: "none"}, "embed")
	effects := []string{}
	c := &forkSubnetCoordinator{store: store, closeConnections: func(map[string]bool) { effects = append(effects, "close") }, invalidate: func() { effects = append(effects, "invalidate") }, reset: func() { effects = append(effects, "reset") }, configureDetection: func(bool, string) {}}
	c.apply(forkSubnetConfig(t, "Corp"))
	cfg := forkSubnetConfig(t, "Corp")
	g, err := outboundgroup.ParseProxyGroup(map[string]any{"name": "Corp", "type": "subnet", "default": "DIRECT", "matches": []any{map[string]any{"type": "wifi", "proxy": "DIRECT"}}}, cfg.Proxies, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Proxies["Corp"] = adapter.NewProxy(g)
	c.apply(cfg)
	if len(effects) != 3 || effects[0] != "close" || effects[1] != "invalidate" || effects[2] != "reset" {
		t.Fatal(effects)
	}
	if c.groups["Corp"].Now() != "DIRECT" {
		t.Fatal("new group visible without new selection")
	}
}

func TestReviewK8ApplyReevaluatesPreviousSameNameGroup(t *testing.T) {
	store := netenv.NewStore(time.Now)
	c := &forkSubnetCoordinator{store: store, closeConnections: func(map[string]bool) {}, invalidate: func() {}, reset: func() {}, configureDetection: func(bool, string) {}}
	before := forkSubnetConfig(t, "Corp")
	c.apply(before)
	old := c.groups["Corp"]
	c.configureDetection = func(bool, string) { _ = store.Report(context.Background(), netenv.Fields{Type: "none"}, "embed") }
	c.apply(forkSubnetConfig(t, "Corp"))
	if old.Now() != "REJECT" {
		t.Fatalf("old same-name group retained stale selection during apply: %s", old.Now())
	}
}
