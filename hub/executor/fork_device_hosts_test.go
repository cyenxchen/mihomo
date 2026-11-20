package executor

import (
	"net/netip"
	"testing"

	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/component/trie"
	"github.com/metacubex/mihomo/config"
)

func TestUpdateHostsPublishesSearchSnapshot(t *testing.T) {
	const alias = "lookup.example"
	newHosts := func(target string, ip netip.Addr) *trie.DomainTrie[resolver.HostValue] {
		t.Helper()
		tree := trie.New[resolver.HostValue]()
		if err := tree.Insert(alias, resolver.HostValue{IsDomain: true, Domain: target}); err != nil {
			t.Fatal(err)
		}
		if err := tree.Insert(target, resolver.HostValue{IPs: []netip.Addr{ip}}); err != nil {
			t.Fatal(err)
		}
		return tree
	}
	oldTree := resolver.DefaultHosts.DomainTrie
	t.Cleanup(func() { updateHosts(oldTree) })
	oldIP := netip.MustParseAddr("192.0.2.1")
	newIP := netip.MustParseAddr("192.0.2.2")
	updateHosts(newHosts("old.example", oldIP))
	replacement := newHosts("new.example", newIP)
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		<-started
		updateHosts(replacement)
		close(done)
	}()
	close(started)
	value, ok := resolver.DefaultHosts.Search(alias, false)
	<-done
	if !ok || len(value.IPs) != 1 || (value.IPs[0] != oldIP && value.IPs[0] != newIP) {
		t.Fatalf("lookup across update returned %v, %t", value, ok)
	}
	value, ok = resolver.DefaultHosts.Search(alias, false)
	if !ok || len(value.IPs) != 1 || value.IPs[0] != newIP {
		t.Fatalf("lookup after update returned %v, %t, want %s", value, ok, newIP)
	}
}

func TestUpdateDNSPublishesSystemHostsFlag(t *testing.T) {
	defaultResolver, defaultMapper, defaultService := resolver.DefaultResolver, resolver.DefaultHostMapper, resolver.DefaultService
	proxyResolver, directResolver := resolver.ProxyServerHostResolver, resolver.DirectHostResolver
	disableSystemHosts, useSystemHosts := resolver.DisableSystemHosts, resolver.UseSystemHosts
	t.Cleanup(func() {
		resolver.DefaultResolver, resolver.DefaultHostMapper, resolver.DefaultService = defaultResolver, defaultMapper, defaultService
		resolver.ProxyServerHostResolver, resolver.DirectHostResolver = proxyResolver, directResolver
		resolver.DisableSystemHosts, resolver.UseSystemHosts = disableSystemHosts, useSystemHosts
	})
	resolver.DisableSystemHosts = false
	resolver.UseSystemHosts = true
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		<-started
		updateDNS(&config.DNS{Enable: true}, false)
		close(done)
	}()
	close(started)
	resolver.DefaultHosts.Search("system-hosts-reload.invalid", false)
	<-done
	if resolver.UseSystemHosts {
		t.Fatal("system hosts lookup remained enabled after DNS update")
	}
}
