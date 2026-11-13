package resolver

// ForkInvalidateNetworkCache synchronously invalidates network-dependent caches.
func ForkInvalidateNetworkCache() {
	for _, r := range []Resolver{DefaultResolver, ProxyServerHostResolver, DirectHostResolver, SystemResolver} {
		// The capability is implemented by dns.Resolver and promoted by dns.Resolvers;
		// using it here avoids a dependency cycle with the DNS package.
		if cache, ok := r.(interface{ ForkInvalidateCache() }); ok {
			cache.ForkInvalidateCache()
		}
	}
}

func ForkResetNetworkConnections() {
	for _, r := range []Resolver{DefaultResolver, ProxyServerHostResolver, DirectHostResolver, SystemResolver} {
		if r != nil {
			r.ResetConnection()
		}
	}
}
