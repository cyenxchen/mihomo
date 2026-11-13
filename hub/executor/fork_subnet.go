package executor

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/component/netenv"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel/statistic"
)

type forkSubnetCoordinator struct {
	mu                 sync.Mutex
	now                func() time.Time
	after              func(time.Duration, func()) func()
	started            map[string]time.Time
	known              bool
	stopTimer          func()
	timerGeneration    uint64
	warnedUnknown      map[string]bool
	store              *netenv.Store
	groups             map[string]*outboundgroup.Subnet
	choices            map[string]string
	diagnostics        map[string]string
	closeConnections   func(map[string]bool)
	invalidate         func()
	reset              func()
	configureDetection func(bool, string)
}

var forkSubnets = &forkSubnetCoordinator{
	store:              netenv.Default,
	closeConnections:   forkCloseSubnetConnections,
	invalidate:         resolver.ForkInvalidateNetworkCache,
	reset:              resolver.ForkResetNetworkConnections,
	configureDetection: netenv.ConfigureDetection,
}

func init() { netenv.Default.Subscribe(forkSubnets.changed) }
func forkCloseSubnetConnections(names map[string]bool) {
	statistic.DefaultManager.Range(func(c statistic.Tracker) bool {
		for name := range names {
			if slices.Contains(c.Info().Chain, name) {
				_ = c.Close()
				break
			}
		}
		return true
	})
}

func (c *forkSubnetCoordinator) apply(cfg *config.Config) {
	groups := map[string]*outboundgroup.Subnet{}
	for name, p := range cfg.Proxies {
		if g, ok := p.Adapter().(*outboundgroup.Subnet); ok {
			groups[name] = g
		}
	}
	c.mu.Lock()
	if c.now == nil {
		c.now = time.Now
	}
	if c.after == nil {
		c.after = func(d time.Duration, f func()) func() { timer := time.AfterFunc(d, f); return func() { timer.Stop() } }
	}
	if c.started == nil {
		c.started = map[string]time.Time{}
		c.warnedUnknown = map[string]bool{}
	}
	for name := range groups {
		if _, exists := c.started[name]; !exists {
			c.started[name] = c.now()
		}
	}
	previous := c.groups
	c.groups = groups
	if c.choices == nil {
		c.choices = map[string]string{}
		c.diagnostics = map[string]string{}
	}
	for name := range c.choices {
		if groups[name] == nil {
			delete(c.choices, name)
			delete(c.diagnostics, name)
		}
	}
	c.mu.Unlock()
	c.configureDetection(len(groups) > 0, cfg.General.Interface)
	c.mu.Lock()
	current := c.store.Current()
	for name, old := range previous {
		if groups[name] != nil && old != groups[name] {
			old.ForkReevaluate(current, c.expired(name, old, current))
		}
	}
	c.mu.Unlock()
	c.changed(c.store.Current())
}

func (c *forkSubnetCoordinator) changed(_ netenv.Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.store.Current()
	changed := map[string]bool{}
	messages := []string{}
	for name, g := range c.groups {
		next := g.ForkReevaluate(current, c.expired(name, g, current))
		old, exists := c.choices[name]
		c.choices[name] = next.Proxy
		if exists && old != next.Proxy {
			changed[name] = true
			messages = append(messages, fmt.Sprintf("%s: %s -> %s; network=%+v", name, old, next.Proxy, current))
		}
		diagnostic := strings.Join(next.Missing, "; ")
		if current.State == netenv.Unknown {
			diagnostic = ""
			if g.UnknownTimedOut() && !c.warnedUnknown[name] {
				diagnostic = "network unknown timeout; selecting default"
				c.warnedUnknown[name] = true
			}
		}
		if diagnostic != c.diagnostics[name] {
			c.diagnostics[name] = diagnostic
			if diagnostic != "" {
				log.Warnln("[Subnet] %s: %s", name, diagnostic)
			}
		}
	}
	c.scheduleUnknown(current)
	if len(changed) == 0 {
		return
	}
	c.closeConnections(changed)
	c.invalidate()
	c.reset()
	for _, message := range messages {
		log.Infoln("[Subnet] %s", message)
	}
}

func ForkSubnetGroups() []map[string]any {
	forkSubnets.mu.Lock()
	defer forkSubnets.mu.Unlock()
	result := []map[string]any{}
	for name, g := range forkSubnets.groups {
		selection := g.Selection()
		var match any = selection.Index
		if selection.Index == 0 {
			match = "default"
		} else if selection.Index < 0 {
			match = "unknown"
		}
		result = append(result, map[string]any{"name": name, "now": selection.Proxy, "match": match, "unknown-timeout": int64(g.UnknownTimeout() / time.Second), "unknown-timed-out": g.UnknownTimedOut()})
	}
	return result
}

// All timer and lifetime bookkeeping is serialized by c.mu. Parsing alone never
// starts a deadline; names retain their original application time for this process.
func (c *forkSubnetCoordinator) expired(name string, g *outboundgroup.Subnet, current netenv.Snapshot) bool {
	return current.State == netenv.Unknown && !c.now().Before(c.started[name].Add(g.UnknownTimeout()))
}

func (c *forkSubnetCoordinator) scheduleUnknown(current netenv.Snapshot) {
	c.timerGeneration++
	generation := c.timerGeneration
	if c.stopTimer != nil {
		c.stopTimer()
		c.stopTimer = nil
	}
	if current.State != netenv.Unknown {
		c.known = true
	}
	if c.known || len(c.groups) == 0 {
		return
	}
	var earliest time.Time
	now := c.now()
	for name, g := range c.groups {
		deadline := c.started[name].Add(g.UnknownTimeout())
		if deadline.After(now) && (earliest.IsZero() || deadline.Before(earliest)) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return
	}
	c.stopTimer = c.after(earliest.Sub(now), func() {
		c.mu.Lock()
		valid := generation == c.timerGeneration
		c.mu.Unlock()
		if valid {
			c.changed(c.store.Current())
		}
	})
}
