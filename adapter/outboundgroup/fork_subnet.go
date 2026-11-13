package outboundgroup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/netenv"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

// Subnet selects solely from a network snapshot; availability never changes it.
type Subnet struct {
	*outbound.Base
	mu              sync.RWMutex
	members         []C.Proxy
	rules           []netenv.Match
	fallback        string
	unknownTimeout  time.Duration
	unknownTimedOut bool
	selection       netenv.Selection
	epoch           uint64
	active          map[*forkSubnetActive]struct{}
	disableUDP      bool
	hidden          bool
	icon            string
	resolve         func(context.Context, string) (netip.Addr, error)
}

var _ ProxyGroup = (*Subnet)(nil)

func forkSubnetUnknownTimeout(raw map[string]any) (time.Duration, error) {
	value, exists := raw["unknown-timeout"]
	if !exists {
		return 10 * time.Second, nil
	}
	seconds, ok := value.(int)
	if !ok || seconds <= 0 || uint64(seconds) > uint64((1<<63-1)/int64(time.Second)) {
		return 0, fmt.Errorf("subnet unknown-timeout must be a positive integer number of seconds")
	}
	return time.Duration(seconds) * time.Second, nil
}
func (s *Subnet) UnknownTimeout() time.Duration { return s.unknownTimeout }
func (s *Subnet) UnknownTimedOut() bool         { s.mu.RLock(); defer s.mu.RUnlock(); return s.unknownTimedOut }
func ForkSubnetMembers(raw map[string]any, validateUser bool) ([]string, error) {
	if _, err := forkSubnetUnknownTimeout(raw); err != nil {
		return nil, err
	}
	if validateUser {
		for k := range raw {
			if k == "proxies" || k == "use" || k == "filter" || k == "exclude-filter" || k == "exclude-type" || k == "include-all" || k == "include-all-proxies" || k == "include-all-providers" {
				return nil, fmt.Errorf("subnet does not accept %s", k)
			}
		}
	}
	fallback, ok := raw["default"].(string)
	if !ok || fallback == "" {
		return nil, fmt.Errorf("subnet requires default")
	}
	rules, err := netenv.ParseMatches(raw["matches"])
	if err != nil {
		return nil, err
	}
	names := []string{}
	seen := map[string]bool{}
	for _, r := range append(rules, netenv.Match{Proxy: fallback}) {
		if !seen[r.Proxy] {
			names = append(names, r.Proxy)
			seen[r.Proxy] = true
		}
	}
	return names, nil
}

func forkParseSubnet(raw map[string]any, option GroupCommonOption, proxies map[string]C.Proxy) (ProxyGroup, error) {
	names, err := ForkSubnetMembers(raw, false)
	if err != nil {
		return nil, err
	}
	for _, k := range []string{"use", "filter", "exclude-filter", "exclude-type", "include-all", "include-all-proxies", "include-all-providers"} {
		if _, ok := raw[k]; ok {
			return nil, fmt.Errorf("subnet does not accept %s", k)
		}
	}
	rules, err := netenv.ParseMatches(raw["matches"])
	if err != nil {
		return nil, err
	}
	timeout, _ := forkSubnetUnknownTimeout(raw)
	s := &Subnet{unknownTimeout: timeout, Base: outbound.NewBase(outbound.BaseOption{Name: option.Name, Type: C.Subnet}), rules: rules, fallback: raw["default"].(string), disableUDP: option.DisableUDP, hidden: option.Hidden, icon: option.Icon, resolve: resolver.ResolveIP}
	for _, name := range names {
		p, ok := proxies[name]
		if !ok {
			return nil, fmt.Errorf("subnet %s: proxy %q not found", option.Name, name)
		}
		s.members = append(s.members, p)
	}
	s.Reevaluate(netenv.Default.Current())
	return s, nil
}

func (s *Subnet) Reevaluate(snapshot netenv.Snapshot) netenv.Selection {
	return s.ForkReevaluate(snapshot, false)
}

func (s *Subnet) ForkReevaluate(snapshot netenv.Snapshot, expired bool) netenv.Selection {
	next := netenv.Select(s.rules, s.fallback, snapshot)
	expired = expired && snapshot.State == netenv.Unknown
	if expired {
		next = netenv.Selection{Proxy: s.fallback}
	}
	s.mu.Lock()
	s.unknownTimedOut = expired
	var previous map[*forkSubnetActive]struct{}
	if next.Proxy != s.selection.Proxy {
		s.epoch++
		previous = s.active
		s.active = make(map[*forkSubnetActive]struct{})
	}
	s.selection = next
	s.mu.Unlock()
	for conn := range previous {
		_ = conn.Close()
	}
	return next
}

func (s *Subnet) Selection() netenv.Selection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.selection
	v.Missing = append([]string(nil), v.Missing...)
	return v
}

func (s *Subnet) selected() (C.Proxy, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.members {
		if p.Name() == s.selection.Proxy {
			return p, s.epoch
		}
	}
	panic("subnet selection not in validated members")
}

func (s *Subnet) stale(epoch uint64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return epoch != s.epoch
}

func (s *Subnet) prepare(ctx context.Context, p C.Proxy, metadata *C.Metadata) (*C.Metadata, error) {
	m := *metadata
	if m.Host != "" && p.IsL3Protocol(&m) {
		ip, err := s.resolve(ctx, m.Host)
		if err != nil {
			return nil, err
		}
		m.DstIP = ip
		m.Host = ""
	}
	return &m, nil
}

func (s *Subnet) DialContext(ctx context.Context, m *C.Metadata) (C.Conn, error) {
	p, epoch := s.selected()
	copy, err := s.prepare(ctx, p, m)
	if err != nil {
		return nil, err
	}
	conn, err := p.DialContext(ctx, copy)
	if err != nil {
		return nil, err
	}
	if s.stale(epoch) {
		_ = conn.Close()
		return nil, fmt.Errorf("subnet %s changed during dial", s.Name())
	}
	conn.AppendToChains(s)
	active, err := s.track(epoch, conn.Close)
	if err != nil {
		return nil, err
	}
	return &forkSubnetConn{Conn: conn, active: active}, nil
}

func (s *Subnet) ListenPacketContext(ctx context.Context, m *C.Metadata) (C.PacketConn, error) {
	p, epoch := s.selected()
	if s.disableUDP || !p.SupportUDP() {
		return nil, fmt.Errorf("subnet %s: UDP is disabled for %s", s.Name(), p.Name())
	}
	copy, err := s.prepare(ctx, p, m)
	if err != nil {
		return nil, err
	}
	conn, err := p.ListenPacketContext(ctx, copy)
	if err != nil {
		return nil, err
	}
	if s.stale(epoch) {
		_ = conn.Close()
		return nil, fmt.Errorf("subnet %s changed during dial", s.Name())
	}
	conn.AppendToChains(s)
	active, err := s.track(epoch, conn.Close)
	if err != nil {
		return nil, err
	}
	return &forkSubnetPacketConn{PacketConn: conn, group: s, proxy: p, epoch: epoch, active: active, l3: p.IsL3Protocol(copy)}, nil
}
func (s *Subnet) SupportUDP() bool                 { return true }
func (s *Subnet) IsL3Protocol(m *C.Metadata) bool  { p, _ := s.selected(); return p.IsL3Protocol(m) }
func (s *Subnet) Unwrap(*C.Metadata, bool) C.Proxy { p, _ := s.selected(); return p }
func (s *Subnet) Now() string                      { return s.Selection().Proxy }
func (s *Subnet) Proxies() []C.Proxy               { return append([]C.Proxy(nil), s.members...) }
func (s *Subnet) Providers() []P.ProxyProvider     { return nil }
func (s *Subnet) Touch()                           {}
func (s *Subnet) Hidden() bool                     { return s.hidden }
func (s *Subnet) Icon() string                     { return s.icon }
func (s *Subnet) URLTest(ctx context.Context, url string, status utils.IntRanges[uint16]) (map[string]uint16, error) {
	results := map[string]uint16{}
	for _, p := range s.members {
		delay, err := p.URLTest(ctx, url, status)
		if err == nil {
			results[p.Name()] = delay
		}
	}
	return results, nil
}

func (s *Subnet) MarshalJSON() ([]byte, error) {
	names := make([]string, 0, len(s.members))
	for _, p := range s.members {
		names = append(names, p.Name())
	}
	return json.Marshal(map[string]any{"type": s.Type().String(), "now": s.Now(), "all": names, "hidden": s.hidden, "icon": s.icon})
}

// Preserve the standard packet interfaces and expose the upstream connection to
// the existing I/O optimizers. Resolution remains tied to this session's choice.
type forkSubnetPacketConn struct {
	active *forkSubnetActive
	l3     bool
	C.PacketConn
	group *Subnet
	proxy C.Proxy
	epoch uint64
}

func (c *forkSubnetPacketConn) ResolveUDP(ctx context.Context, m *C.Metadata) error {
	if c.group.stale(c.epoch) {
		return fmt.Errorf("subnet %s changed during UDP session", c.group.Name())
	}
	if !c.l3 {
		return c.PacketConn.ResolveUDP(ctx, m)
	}
	next, err := c.group.prepare(ctx, c.proxy, m)
	if err != nil {
		return err
	}
	if c.group.stale(c.epoch) {
		return fmt.Errorf("subnet %s changed during UDP resolution", c.group.Name())
	}
	*m = *next
	return nil
}
func (c *forkSubnetPacketConn) Upstream() any           { return c.PacketConn }
func (c *forkSubnetPacketConn) ReaderReplaceable() bool { return true }
func (c *forkSubnetPacketConn) WriterReplaceable() bool { return true }

// Registration and epoch advancement share s.mu, closing the tracker handoff gap.
func (s *Subnet) track(epoch uint64, closeFn func() error) (*forkSubnetActive, error) {
	s.mu.Lock()
	if s.epoch != epoch {
		s.mu.Unlock()
		_ = closeFn()
		return nil, fmt.Errorf("subnet %s changed during dial", s.Name())
	}
	a := &forkSubnetActive{group: s, close: closeFn}
	if s.active == nil {
		s.active = make(map[*forkSubnetActive]struct{})
	}
	s.active[a] = struct{}{}
	s.mu.Unlock()
	return a, nil
}

type forkSubnetActive struct {
	group *Subnet
	close func() error
	once  sync.Once
	err   error
}

func (a *forkSubnetActive) Close() error {
	a.once.Do(func() { a.group.mu.Lock(); delete(a.group.active, a); a.group.mu.Unlock(); a.err = a.close() })
	return a.err
}

type forkSubnetConn struct {
	C.Conn
	active *forkSubnetActive
}

func (c *forkSubnetConn) Close() error            { return c.active.Close() }
func (c *forkSubnetConn) Upstream() any           { return c.Conn }
func (c *forkSubnetConn) ReaderReplaceable() bool { return true }
func (c *forkSubnetConn) WriterReplaceable() bool { return true }
func (c *forkSubnetPacketConn) Close() error      { return c.active.Close() }
