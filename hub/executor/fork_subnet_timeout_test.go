package executor

import (
	"context"
	"testing"
	"time"

	"github.com/metacubex/mihomo/component/netenv"
)

type forkTimeoutTask struct {
	at      time.Time
	run     func()
	stopped bool
}
type forkTimeoutClock struct {
	now   time.Time
	tasks []*forkTimeoutTask
}

func (f *forkTimeoutClock) after(d time.Duration, run func()) func() {
	task := &forkTimeoutTask{at: f.now.Add(d), run: run}
	f.tasks = append(f.tasks, task)
	return func() { task.stopped = true }
}

func (f *forkTimeoutClock) advance(d time.Duration) {
	f.now = f.now.Add(d)
	for {
		ran := false
		for _, task := range f.tasks {
			if !task.stopped && !task.at.After(f.now) {
				task.stopped = true
				task.run()
				ran = true
			}
		}
		if !ran {
			return
		}
	}
}

func (f *forkTimeoutClock) pending() int {
	n := 0
	for _, task := range f.tasks {
		if !task.stopped {
			n++
		}
	}
	return n
}

func TestReviewU1UnknownDeadlineActivelySwitchesAndSurvivesReload(t *testing.T) {
	clock := &forkTimeoutClock{now: time.Unix(100, 0)}
	store := netenv.NewStore(func() time.Time { return clock.now })
	effects := []string{}
	c := &forkSubnetCoordinator{store: store, now: func() time.Time { return clock.now }, after: clock.after, closeConnections: func(map[string]bool) { effects = append(effects, "close") }, invalidate: func() { effects = append(effects, "invalidate") }, reset: func() { effects = append(effects, "reset") }, configureDetection: func(bool, string) {}}
	cancel := store.Subscribe(c.changed)
	defer cancel()
	c.apply(forkSubnetConfig(t, "Corp"))
	clock.advance(9 * time.Second)
	if c.groups["Corp"].Now() != "DIRECT" || len(effects) != 0 {
		t.Fatal("grace period ignored")
	}
	c.apply(forkSubnetConfig(t, "Corp"))
	clock.advance(time.Second)
	if c.groups["Corp"].Now() != "REJECT" || len(effects) != 3 {
		t.Fatalf("deadline did not switch actively: now=%s effects=%v", c.groups["Corp"].Now(), effects)
	}
	if effects[0] != "close" || effects[1] != "invalidate" || effects[2] != "reset" {
		t.Fatal(effects)
	}
	if clock.pending() != 0 {
		t.Fatal("expired unknown timer retained")
	}
	c.apply(forkSubnetConfig(t))
	c.apply(forkSubnetConfig(t, "Corp"))
	if c.groups["Corp"].Now() != "REJECT" {
		t.Fatal("reappearing name reset process lifetime deadline")
	}
}

func TestReviewU1KnownSnapshotCancelsTimer(t *testing.T) {
	clock := &forkTimeoutClock{now: time.Unix(100, 0)}
	store := netenv.NewStore(func() time.Time { return clock.now })
	effects := 0
	c := &forkSubnetCoordinator{store: store, now: func() time.Time { return clock.now }, after: clock.after, closeConnections: func(map[string]bool) { effects++ }, invalidate: func() {}, reset: func() {}, configureDetection: func(bool, string) {}}
	cancel := store.Subscribe(c.changed)
	defer cancel()
	c.apply(forkSubnetConfig(t, "Corp"))
	if clock.pending() != 1 {
		t.Fatal("unknown deadline not scheduled")
	}
	clock.advance(time.Second)
	_ = store.Report(context.Background(), netenv.Fields{Type: "wifi"}, "embed")
	if clock.pending() != 0 {
		t.Fatal("known snapshot left unknown timer running")
	}
	clock.advance(time.Minute)
	if c.groups["Corp"].Now() != "DIRECT" || effects != 0 {
		t.Fatal("known network switched on unknown timer")
	}
}

func TestReviewU1NewNameGetsOwnDeadlineAndAPIStatus(t *testing.T) {
	clock := &forkTimeoutClock{now: time.Unix(100, 0)}
	store := netenv.NewStore(func() time.Time { return clock.now })
	c := &forkSubnetCoordinator{store: store, now: func() time.Time { return clock.now }, after: clock.after, closeConnections: func(map[string]bool) {}, invalidate: func() {}, reset: func() {}, configureDetection: func(bool, string) {}}
	c.apply(forkSubnetConfig(t, "Corp"))
	clock.advance(10 * time.Second)
	c.apply(forkSubnetConfig(t, "Corp", "New"))
	if c.groups["Corp"].Now() != "REJECT" || c.groups["New"].Now() != "DIRECT" {
		t.Fatal("new name reused another group's start time")
	}
	previous := forkSubnets
	forkSubnets = c
	defer func() { forkSubnets = previous }()
	for _, g := range ForkSubnetGroups() {
		want := g["name"] == "Corp"
		if g["unknown-timed-out"] != want || g["unknown-timeout"] != int64(10) {
			t.Fatalf("invalid diagnostic: %+v", g)
		}
	}
	clock.advance(10 * time.Second)
	if c.groups["New"].Now() != "REJECT" || clock.pending() != 0 {
		t.Fatal("new name deadline not fired")
	}
}

func TestReviewU1PreStartUnknownNotificationNeedsNoClock(t *testing.T) {
	c := &forkSubnetCoordinator{store: netenv.NewStore(time.Now)}
	c.changed(c.store.Current())
	if c.stopTimer != nil {
		t.Fatal("timer scheduled without configured groups")
	}
}
