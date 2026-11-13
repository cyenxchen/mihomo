package netenv

import (
	"context"
	"net/netip"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/component/dialer"
	"github.com/metacubex/mihomo/log"
)

type detectionController struct {
	mu             sync.Mutex
	store          *Store
	probe          func(context.Context, string) (Snapshot, error)
	events         func(func()) func()
	poll           <-chan time.Time
	initialTimeout func() (<-chan time.Time, func())
	cancel         context.CancelFunc
	stopEvents     func()
	interfaceName  string
}

var networkDetection = &detectionController{store: Default, probe: detectPlatform, events: monitorNetworkChanges, initialTimeout: func() (<-chan time.Time, func()) {
	t := time.NewTimer(2 * time.Second)
	return t.C, func() { t.Stop() }
}}

func ConfigureDetection(enabled bool, interfaceName string) {
	networkDetection.configure(enabled && platformDetectionSupported(), interfaceName)
}

func (c *detectionController) configure(enabled bool, interfaceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil && enabled && c.interfaceName == interfaceName {
		return
	}
	if c.cancel != nil {
		c.cancel()
		c.stopEvents()
		c.cancel = nil
	}
	if !enabled {
		c.store.StopDetection()
		return
	}
	c.interfaceName = interfaceName
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	probe := func(parent context.Context) (Snapshot, error) {
		timeout, stop := context.WithTimeout(parent, 5*time.Second)
		defer stop()
		name := interfaceName
		if name == "" {
			if finder := dialer.DefaultInterfaceFinder.Load(); finder != nil {
				name = resolveInterfaceHint("", finder.FindInterfaceName(netip.Addr{}))
			}
		}
		return c.probe(timeout, name)
	}
	c.store.SetDetector(probe)
	signal := make(chan struct{}, 1)
	c.stopEvents = c.events(func() {
		select {
		case signal <- struct{}{}:
		default:
		}
	})
	poll := c.poll
	var ticker *time.Ticker
	if poll == nil {
		ticker = time.NewTicker(10 * time.Second)
		poll = ticker.C
	}
	initial := make(chan struct{})
	go func() { _ = c.store.Detect(ctx); close(initial) }()
	go func() {
		if ticker != nil {
			defer ticker.Stop()
		}
		var debounce *time.Timer
		var pending <-chan time.Time
		defer func() {
			if debounce != nil {
				debounce.Stop()
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-signal:
				if debounce == nil {
					debounce = time.NewTimer(time.Second)
				} else {
					if !debounce.Stop() {
						select {
						case <-debounce.C:
						default:
						}
					}
					debounce.Reset(time.Second)
				}
				pending = debounce.C
			case <-pending:
				pending = nil
				_ = c.store.Detect(ctx)
			case <-poll:
				_ = c.store.Detect(ctx)
			}
		}
	}()
	timeout, stop := c.initialTimeout()
	defer stop()
	select {
	case <-initial:
	case <-timeout:
		log.Warnln("[Subnet] initial network detection timed out; retaining last known result")
	}
}

func readCommand(ctx context.Context, path string, args ...string) string {
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func detectedSnapshot(fields Fields) Snapshot {
	raw := map[string]any{}
	for i, value := range fields.values() {
		if value != "" {
			single, err := ParseFields(map[string]any{fieldNames[i]: value}, true)
			if err == nil {
				raw[fieldNames[i]] = single.values()[i]
			}
		}
	}
	normalized, _ := ParseFields(raw, true)
	return Snapshot{State: Online, Fields: normalized}
}
