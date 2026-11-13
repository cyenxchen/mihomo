package dns

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/resolver"

	D "github.com/miekg/dns"
)

// Pause an already successful DNS response immediately before cache insertion.
// This deterministically represents a goroutine being descheduled after network
// I/O has completed. Closing its former transport cannot revoke the response.
type forkSubnetCacheGate struct {
	dnsCache
	ready  chan struct{}
	resume chan struct{}
	paused atomic.Bool
}

func (c *forkSubnetCacheGate) SetWithExpire(key string, value *D.Msg, expire time.Time) {
	if c.paused.CompareAndSwap(false, true) {
		close(c.ready)
		<-c.resume
	}
	c.dnsCache.SetWithExpire(key, value, expire)
}

type forkSubnetNetworkDNS struct {
	calls  atomic.Int32
	resets atomic.Int32
	ready  chan struct{}
	resume chan struct{}
}

func (c *forkSubnetNetworkDNS) Address() string  { return "test://network-switch" }
func (c *forkSubnetNetworkDNS) ResetConnection() { c.resets.Add(1) }
func (c *forkSubnetNetworkDNS) ExchangeContext(_ context.Context, q *D.Msg) (*D.Msg, error) {
	ip := net.IPv4(10, 1, 0, 1)
	if c.calls.Add(1) > 1 {
		ip = net.IPv4(10, 2, 0, 1)
	}
	reply := new(D.Msg)
	reply.SetReply(q)
	reply.Answer = []D.RR{&D.A{Hdr: D.RR_Header{Name: q.Question[0].Name, Rrtype: D.TypeA, Class: D.ClassINET, Ttl: 600}, A: ip}}
	if c.ready != nil && ip.Equal(net.IPv4(10, 1, 0, 1)) {
		close(c.ready)
		<-c.resume
	}
	return reply, nil
}

func TestForkSubnetClearCacheRejectsCompletedOldResponse(t *testing.T) {
	client := &forkSubnetNetworkDNS{ready: make(chan struct{}), resume: make(chan struct{})}
	r := NewResolverFromClient(client)
	finished := make(chan error, 1)
	go func() { _, err := r.LookupIPv4(context.Background(), "corp.example"); finished <- err }()
	<-client.ready
	// The old response has already left the transport. The design's tracker
	// close cannot affect it; apply the remaining clear/reset operations now.
	r.ClearCache()
	r.ResetConnection()
	close(client.resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	ips, err := r.LookupIPv4(context.Background(), "corp.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || ips[0].String() != "10.2.0.1" {
		t.Fatalf("old network DNS response repopulated cache after ClearCache/ResetConnection: got %v, want [10.2.0.1]; exchanges=%d resets=%d", ips, client.calls.Load(), client.resets.Load())
	}
}

// Done is first called after DoChan registered this request. This barrier
// proves ordering without waiting for a timer or guessing goroutine progress.
type forkSubnetQueryContext struct {
	context.Context
	registered chan struct{}
	once       atomic.Bool
}

func (c *forkSubnetQueryContext) Done() <-chan struct{} {
	if c.once.CompareAndSwap(false, true) {
		close(c.registered)
	}
	return nil
}

func TestForkSubnetNewQueryDoesNotJoinOldFlight(t *testing.T) {
	client := &forkSubnetNetworkDNS{ready: make(chan struct{}), resume: make(chan struct{})}
	r := NewResolverFromClient(client)
	old := make(chan error, 1)
	go func() { _, err := r.LookupIPv4(context.Background(), "corp.example"); old <- err }()
	<-client.ready
	r.ClearCache()
	ctx := &forkSubnetQueryContext{Context: context.Background(), registered: make(chan struct{})}
	fresh := make(chan string, 1)
	go func() {
		ips, err := r.LookupIPv4(ctx, "corp.example")
		if err != nil {
			fresh <- err.Error()
		} else {
			fresh <- ips[0].String()
		}
	}()
	<-ctx.registered
	close(client.resume)
	if err := <-old; err != nil {
		t.Fatal(err)
	}
	if got := <-fresh; got != "10.2.0.1" {
		t.Fatalf("new query joined old flight: got %s", got)
	}
}

func TestForkSubnetInvalidationSerializesCacheWrite(t *testing.T) {
	r := NewResolverFromClient(&forkSubnetNetworkDNS{})
	gate := &forkSubnetCacheGate{dnsCache: r.cache, ready: make(chan struct{}), resume: make(chan struct{})}
	r.cache = gate
	finished := make(chan error, 1)
	go func() { _, err := r.LookupIPv4(context.Background(), "corp.example"); finished <- err }()
	<-gate.ready
	// TryLock establishes whether invalidation can pass an ongoing cache write.
	unprotected := r.forkEpoch.mu.TryLock()
	if unprotected {
		r.forkEpoch.mu.Unlock()
		r.ClearCache()
	}
	invalidated := make(chan struct{})
	if !unprotected {
		go func() { r.ClearCache(); close(invalidated) }()
	}
	close(gate.resume)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if !unprotected {
		<-invalidated
	}
	q := D.Question{Name: "corp.example.", Qtype: D.TypeA, Qclass: D.ClassINET}
	if _, _, hit := r.cache.GetWithExpire(q.String()); hit {
		t.Fatal("invalidation raced cache write: old response remains cached")
	}
}

func TestForkSubnetInvalidationCoversResolverSlots(t *testing.T) {
	oldMain, oldProxy, oldDirect, oldSystem := resolver.DefaultResolver, resolver.ProxyServerHostResolver, resolver.DirectHostResolver, resolver.SystemResolver
	defer func() {
		resolver.DefaultResolver, resolver.ProxyServerHostResolver, resolver.DirectHostResolver, resolver.SystemResolver = oldMain, oldProxy, oldDirect, oldSystem
	}()
	main, proxy, direct, system := NewResolverFromClient(&forkSubnetNetworkDNS{}), NewResolverFromClient(&forkSubnetNetworkDNS{}), NewResolverFromClient(&forkSubnetNetworkDNS{}), NewResolverFromClient(&forkSubnetNetworkDNS{})
	resolver.DefaultResolver = Resolvers{Resolver: main, ProxyResolver: proxy, DirectResolver: direct}
	resolver.ProxyServerHostResolver = proxy
	resolver.DirectHostResolver = direct
	resolver.SystemResolver = system
	for _, r := range []*Resolver{main, proxy, direct, system} {
		if _, err := r.LookupIPv4(context.Background(), "corp.example"); err != nil {
			t.Fatal(err)
		}
	}
	resolver.ForkInvalidateNetworkCache()
	for i, r := range []*Resolver{main, proxy, direct, system} {
		ips, err := r.LookupIPv4(context.Background(), "corp.example")
		if err != nil || ips[0].String() != "10.2.0.1" {
			t.Fatalf("slot %d not invalidated: %v %v", i, ips, err)
		}
	}
}

func TestReviewK7InvalidatesBootstrapResolver(t *testing.T) {
	parent := NewResolverFromClient(&forkSubnetNetworkDNS{})
	bootstrap := NewResolverFromClient(&forkSubnetNetworkDNS{})
	parent.defaultResolver = bootstrap
	if _, err := bootstrap.LookupIPv4(context.Background(), "bootstrap.example"); err != nil {
		t.Fatal(err)
	}
	parent.ClearCache()
	ips, err := bootstrap.LookupIPv4(context.Background(), "bootstrap.example")
	if err != nil {
		t.Fatal(err)
	}
	if ips[0].String() != "10.2.0.1" {
		t.Fatalf("bootstrap retained previous network cache: %v", ips)
	}
}
