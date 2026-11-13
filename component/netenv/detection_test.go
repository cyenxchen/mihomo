package netenv

import (
	"context"
	"testing"
	"time"
)

func TestSubnetDetectionLifecycle(t *testing.T) {
	store := NewStore(time.Now)
	calls := 0
	controller := &detectionController{store: store, probe: func(_ context.Context, name string) (Snapshot, error) {
		calls++
		return Snapshot{State: Online, Fields: Fields{Interface: name, Type: "wired"}}, nil
	}, events: func(func()) func() { return func() {} }, poll: make(chan time.Time), initialTimeout: func() (<-chan time.Time, func()) { return nil, func() {} }}
	controller.configure(true, "en7")
	if store.Current().Interface != "en7" || calls != 1 {
		t.Fatal("initial detection not completed before visibility")
	}
	controller.configure(false, "")
	if store.View().Detect.Valid || store.Current().Interface != "en7" {
		t.Fatal("stop did not preserve last known result and invalidate slot")
	}
	controller.configure(true, "en8")
	if store.Current().Interface != "en8" || calls != 2 {
		t.Fatal("restart did not detect before visibility")
	}
	controller.configure(false, "")
}

func TestSubnetInitialTimeoutRetainsAndThenRefreshes(t *testing.T) {
	store := NewStore(time.Now)
	_ = store.Report(context.Background(), Fields{Interface: "old"}, "embed")
	store.ClearReport()
	release, updated := make(chan struct{}), make(chan struct{}, 1)
	store.Subscribe(func(s Snapshot) {
		if s.Interface == "new" {
			updated <- struct{}{}
		}
	})
	timeout := make(chan time.Time, 1)
	timeout <- time.Time{}
	c := &detectionController{store: store, probe: func(context.Context, string) (Snapshot, error) {
		<-release
		return Snapshot{State: Online, Fields: Fields{Interface: "new"}}, nil
	}, events: func(func()) func() { return func() {} }, poll: make(chan time.Time), initialTimeout: func() (<-chan time.Time, func()) { return timeout, func() {} }}
	c.configure(true, "")
	if store.Current().Interface != "old" {
		t.Fatal("timeout lost last known result")
	}
	close(release)
	<-updated
	if store.Current().Interface != "new" {
		t.Fatal("late detection not published")
	}
	c.configure(false, "")
}

func TestReviewK6InterfaceReloadKeepsDetectionSlot(t *testing.T) {
	s := NewStore(time.Now)
	c := &detectionController{store: s, probe: func(_ context.Context, name string) (Snapshot, error) {
		return Snapshot{State: Online, Fields: Fields{Interface: name, Gateway: "10.15.67.254"}}, nil
	}, events: func(func()) func() { return func() {} }, poll: make(chan time.Time), initialTimeout: func() (<-chan time.Time, func()) { return nil, func() {} }}
	c.configure(true, "en0")
	defer c.configure(false, "")
	_ = s.Report(context.Background(), Fields{SSID: "GOLDWIND"}, "rest")
	var observations []Snapshot
	cancel := s.Subscribe(func(v Snapshot) { observations = append(observations, v) })
	defer cancel()
	c.configure(true, "en1")
	for _, v := range observations {
		if v.Gateway == "" {
			t.Fatalf("reload temporarily discarded detect slot: %+v", observations)
		}
	}
	if !s.View().Detect.Valid || s.Current().Interface != "en1" {
		t.Fatal(s.View())
	}
}
