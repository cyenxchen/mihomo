package outboundgroup

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"testing"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/netenv"
	C "github.com/metacubex/mihomo/constant"
)

type forkSubnetFake struct {
	*outbound.Base
	calls  int
	l3     bool
	dial   func(*C.Metadata) (C.Conn, error)
	packet func(*C.Metadata) (C.PacketConn, error)
}

func (f *forkSubnetFake) IsL3Protocol(*C.Metadata) bool { return f.l3 }
func (f *forkSubnetFake) DialContext(_ context.Context, m *C.Metadata) (C.Conn, error) {
	f.calls++
	if f.dial != nil {
		return f.dial(m)
	}
	return nil, errors.New("offline")
}

func (f *forkSubnetFake) ListenPacketContext(_ context.Context, m *C.Metadata) (C.PacketConn, error) {
	if f.packet != nil {
		return f.packet(m)
	}
	f.calls++
	return nil, errors.New("offline")
}

func forkSubnetFixture(t *testing.T) (ProxyGroup, *forkSubnetFake, *forkSubnetFake) {
	t.Helper()
	a := &forkSubnetFake{Base: outbound.NewBase(outbound.BaseOption{Name: "LAN", UDP: true})}
	b := &forkSubnetFake{Base: outbound.NewBase(outbound.BaseOption{Name: "TAIL"})}
	g, err := ParseProxyGroup(map[string]any{"name": "Corp", "type": "subnet", "default": "TAIL", "matches": []any{map[string]any{"ssid": "GOLDWIND", "proxy": "LAN"}}}, map[string]C.Proxy{"LAN": adapter.NewProxy(a), "TAIL": adapter.NewProxy(b)}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return g, a, b
}

func TestForkSubnetInitialAndUDP(t *testing.T) {
	g, a, b := forkSubnetFixture(t)
	if g.Now() != "LAN" {
		t.Fatalf("unknown choice=%s", g.Now())
	}
	if _, ok := g.(SelectAble); ok {
		t.Fatal("subnet must not be manually selectable")
	}
	if !g.SupportUDP() {
		t.Fatal("rule would skip subnet")
	}
	// Public snapshot callback is deliberately absent until configuration application.
	g.(interface {
		Reevaluate(netenv.Snapshot) netenv.Selection
	}).Reevaluate(netenv.Snapshot{State: netenv.None})
	if g.Now() != "TAIL" {
		t.Fatal("none must use default")
	}
	if _, err := g.ListenPacketContext(context.Background(), &C.Metadata{}); err == nil {
		t.Fatal("UDP should fail")
	}
	if a.calls != 0 || b.calls != 0 {
		t.Fatal("unsupported UDP reached outbound")
	}
	if _, err := g.DialContext(context.Background(), &C.Metadata{}); err == nil {
		t.Fatal("offline should fail")
	}
	if a.calls != 0 || b.calls != 1 {
		t.Fatal("failure fell back")
	}
}

func TestForkSubnetPreResolveAndRecheck(t *testing.T) {
	g, a, _ := forkSubnetFixture(t)
	a.l3 = true
	configurable := g.(interface {
		setForkResolver(func(context.Context, string) (netip.Addr, error))
	})
	configurable.setForkResolver(func(_ context.Context, host string) (netip.Addr, error) {
		if host != "corp.example" {
			t.Fatal(host)
		}
		return netip.MustParseAddr("10.1.2.3"), nil
	})
	input := &C.Metadata{Host: "corp.example", DstPort: 443}
	conn := &forkSubnetTCP{}
	a.dial = func(m *C.Metadata) (C.Conn, error) {
		if m.Host != "" || m.DstIP.String() != "10.1.2.3" {
			t.Fatalf("not pre-resolved: %+v", m)
		}
		g.(interface {
			Reevaluate(netenv.Snapshot) netenv.Selection
		}).Reevaluate(netenv.Snapshot{State: netenv.None})
		return conn, nil
	}
	if _, err := g.DialContext(context.Background(), input); err == nil {
		t.Fatal("stale dial escaped")
	}
	if input.Host != "corp.example" || input.DstIP.IsValid() {
		t.Fatal("metadata mutated")
	}
	if !conn.closed {
		t.Fatal("stale connection open")
	}
}

func (s *Subnet) setForkResolver(fn func(context.Context, string) (netip.Addr, error)) {
	s.resolve = fn
}

type forkSubnetPacket struct {
	C.PacketConn
	closed   bool
	resolved bool
}

func (p *forkSubnetPacket) Close() error                  { p.closed = true; return nil }
func (p *forkSubnetPacket) AppendToChains(C.ProxyAdapter) {}
func (p *forkSubnetPacket) ResolveUDP(context.Context, *C.Metadata) error {
	p.resolved = true
	return nil
}

func TestForkSubnetUDPLaterDomain(t *testing.T) {
	g, a, _ := forkSubnetFixture(t)
	s := g.(*Subnet)
	a.l3 = true
	s.resolve = func(context.Context, string) (netip.Addr, error) { return netip.MustParseAddr("10.4.5.6"), nil }
	pc := &forkSubnetPacket{}
	a.packet = func(*C.Metadata) (C.PacketConn, error) { return pc, nil }
	conn, err := g.ListenPacketContext(context.Background(), &C.Metadata{Host: "first.example"})
	if err != nil {
		t.Fatal(err)
	}
	m := &C.Metadata{Host: "second.example"}
	if err = conn.ResolveUDP(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if m.Host != "" || m.DstIP.String() != "10.4.5.6" || pc.resolved {
		t.Fatalf("later UDP domain bypassed policy: %+v underlying=%v", m, pc.resolved)
	}
}

func TestForkSubnetUDPStaleAndDisabled(t *testing.T) {
	g, a, _ := forkSubnetFixture(t)
	s := g.(*Subnet)
	pc := &forkSubnetPacket{}
	a.packet = func(*C.Metadata) (C.PacketConn, error) {
		s.Reevaluate(netenv.Snapshot{State: netenv.None})
		return pc, nil
	}
	if _, err := s.ListenPacketContext(context.Background(), &C.Metadata{}); err == nil || !pc.closed {
		t.Fatal("stale UDP connection escaped")
	}
	s.Reevaluate(netenv.Snapshot{State: netenv.Unknown})
	s.disableUDP = true
	a.packet = func(*C.Metadata) (C.PacketConn, error) { t.Fatal("disabled UDP dialed"); return nil, nil }
	if _, err := s.ListenPacketContext(context.Background(), &C.Metadata{}); err == nil {
		t.Fatal("disabled UDP accepted")
	}
}

type forkSubnetTCP struct {
	C.Conn
	closed bool
}

func (c *forkSubnetTCP) Close() error                  { c.closed = true; return nil }
func (c *forkSubnetTCP) AppendToChains(C.ProxyAdapter) {}
func TestForkSubnetResolutionFailureDoesNotDial(t *testing.T) {
	g, a, b := forkSubnetFixture(t)
	a.l3 = true
	g.(*Subnet).resolve = func(context.Context, string) (netip.Addr, error) {
		return netip.Addr{}, errors.New("DNS policy failed")
	}
	if _, err := g.DialContext(context.Background(), &C.Metadata{Host: "corp.example"}); err == nil {
		t.Fatal("DNS failure ignored")
	}
	if a.calls != 0 || b.calls != 0 {
		t.Fatal("DNS failure dialed outbound")
	}
}

func TestReviewK3ReturnedConnectionsClosedBeforeTrackerRegistration(t *testing.T) {
	for _, udp := range []bool{false, true} {
		t.Run(fmt.Sprint(udp), func(t *testing.T) {
			g, a, _ := forkSubnetFixture(t)
			s := g.(*Subnet)
			tcp := &forkSubnetTCP{}
			packet := &forkSubnetPacket{}
			a.dial = func(*C.Metadata) (C.Conn, error) { return tcp, nil }
			a.packet = func(*C.Metadata) (C.PacketConn, error) { return packet, nil }
			var closeConn func() error
			if udp {
				c, err := s.ListenPacketContext(context.Background(), &C.Metadata{})
				if err != nil {
					t.Fatal(err)
				}
				closeConn = c.Close
			} else {
				c, err := s.DialContext(context.Background(), &C.Metadata{})
				if err != nil {
					t.Fatal(err)
				}
				closeConn = c.Close
			}
			// No statistic tracker exists yet. Switching must still close the connection.
			s.Reevaluate(netenv.Snapshot{State: netenv.None})
			if udp && !packet.closed || !udp && !tcp.closed {
				t.Fatal("old connection escaped the tracker registration gap")
			}
			if err := closeConn(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReviewU1UnknownTimeoutValidation(t *testing.T) {
	for _, v := range []any{0, -1, 1.5, 10.0, "10", true, nil, int(^uint(0) >> 1)} {
		raw := map[string]any{"name": "Corp", "type": "subnet", "default": "DIRECT", "matches": []any{map[string]any{"type": "wifi", "proxy": "DIRECT"}}, "unknown-timeout": v}
		if _, err := ParseProxyGroup(raw, map[string]C.Proxy{"DIRECT": adapter.NewProxy(outbound.NewDirect())}, nil, nil, nil); err == nil {
			t.Fatalf("accepted invalid unknown-timeout=%#v", v)
		}
	}
}

func TestReviewK3CloseRemovesOwnRegistration(t *testing.T) {
	g, a, _ := forkSubnetFixture(t)
	s := g.(*Subnet)
	tcp := &forkSubnetTCP{}
	a.dial = func(*C.Metadata) (C.Conn, error) { return tcp, nil }
	conn, err := s.DialContext(context.Background(), &C.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.active) != 1 {
		t.Fatal("connection not registered")
	}
	_ = conn.Close()
	_ = conn.Close()
	if len(s.active) != 0 {
		t.Fatal("closed connection retained")
	}
	calls := 0
	_, epoch := s.selected()
	entry, err := s.track(epoch, func() error { calls++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	_ = entry.Close()
	_ = entry.Close()
	if calls != 1 {
		t.Fatalf("underlying close called %d times", calls)
	}
}
