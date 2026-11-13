package dns

import (
	"strconv"
	"sync"
	"sync/atomic"

	D "github.com/miekg/dns"
)

type forkCacheEpoch struct {
	mu    sync.RWMutex
	value atomic.Uint64
}

func (r *Resolver) forkQueryEpoch() uint64 { return r.forkEpoch.value.Load() }
func (r *Resolver) forkPutMsgToCache(epoch uint64, q D.Question, msg *D.Msg) {
	r.forkEpoch.mu.RLock()
	defer r.forkEpoch.mu.RUnlock()
	if epoch == r.forkEpoch.value.Load() {
		putMsgToCache(r.cache, q, msg)
	}
}

// ForkInvalidateCache also invalidates results of queries already in flight.
func (r *Resolver) ForkInvalidateCache() {
	seen := map[*Resolver]bool{}
	for current := r; current != nil && !seen[current]; current = current.defaultResolver {
		seen[current] = true
		current.forkEpoch.mu.Lock()
		current.forkEpoch.value.Add(1)
		if current.cache != nil {
			current.cache.Clear()
		}
		current.forkEpoch.mu.Unlock()
	}
}

func (r *Resolver) forkQueryKey(q D.Question) (uint64, string) {
	epoch := r.forkQueryEpoch()
	return epoch, q.String() + "/" + strconv.FormatUint(epoch, 10)
}
