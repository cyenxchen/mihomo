package netenv

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubnetStateRetention(t *testing.T) {
	s := NewStore(func() time.Time { return time.Unix(42, 0) })
	if s.Current().State != Unknown {
		t.Fatal("initial state must be unknown")
	}
	if err := s.Report(context.Background(), Fields{Type: "wifi", SSID: "GOLDWIND"}, "embed"); err != nil {
		t.Fatal(err)
	}
	if s.Current().SSID != "GOLDWIND" {
		t.Fatal("report not applied")
	}
	s.ClearReport()
	s.StopDetection()
	if s.Current().SSID != "GOLDWIND" || s.View().Report.Valid || s.View().Detect.Valid {
		t.Fatal("last known result lost or slots remain valid")
	}
	s.SetDetector(func(context.Context) (Snapshot, error) { return Snapshot{}, errors.New("timeout") })
	_ = s.Detect(context.Background())
	if s.Current().SSID != "GOLDWIND" {
		t.Fatal("failure discarded known result")
	}
	s.SetDetector(func(context.Context) (Snapshot, error) { return Snapshot{State: None}, nil })
	if err := s.Detect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Current().State != None {
		t.Fatal("disconnection must be known none")
	}
}

func TestSubnetReportBinding(t *testing.T) {
	s := NewStore(time.Now)
	detected := Snapshot{State: Online, Fields: Fields{Type: "wifi", Interface: "en0", Gateway: "10.1.1.1", DHCPServer: "10.1.1.2"}}
	fail := false
	s.SetDetector(func(context.Context) (Snapshot, error) {
		if fail {
			return Snapshot{}, errors.New("timeout")
		}
		return detected, nil
	})
	report := func(f Fields, source string) {
		t.Helper()
		if err := s.Report(context.Background(), f, source); err != nil {
			t.Fatal(err)
		}
	}
	report(Fields{SSID: "GOLDWIND"}, "rest")
	if s.Current().SSID != "GOLDWIND" || s.Current().Gateway != "10.1.1.1" {
		t.Fatal("merge failed")
	}
	detected.DHCPServer = ""
	_ = s.Detect(context.Background())
	if !s.View().Report.Valid {
		t.Fatal("unavailable identity field expired report")
	}
	detected.Gateway = "192.168.1.1"
	_ = s.Detect(context.Background())
	if s.View().Report.Valid || s.Current().SSID != "" {
		t.Fatal("old SSID survived network change")
	}
	report(Fields{SSID: "HOME"}, "rest")
	_ = s.Detect(context.Background())
	if s.Current().SSID != "HOME" {
		t.Fatal("new report bound to old identity")
	}
	report(Fields{SSID: "NEW"}, "embed")
	if s.Current().SSID != "NEW" || s.View().Report.Source != "embed" {
		t.Fatal("last writer did not win")
	}
	s.ClearReport()
	if s.Current().SSID != "" || s.Current().Gateway != "192.168.1.1" {
		t.Fatal("clear did not restore detection")
	}
	report(Fields{Interface: "en9", SSID: "BAD"}, "rest")
	if s.View().Report.Valid || s.Current().SSID != "" {
		t.Fatal("wrong interface report accepted")
	}
	fail = true
	report(Fields{SSID: "PENDING"}, "rest")
	fail = false
	detected.Gateway = "172.16.1.1"
	_ = s.Detect(context.Background())
	if s.View().Report.Valid {
		t.Fatal("failed inline detection must bind the last valid identity")
	}
	report(Fields{SSID: "FRESH"}, "rest")
	detected.Gateway = "172.16.2.1"
	_ = s.Detect(context.Background())
	if s.View().Report.Valid {
		t.Fatal("bound report did not expire")
	}
}

func TestSubnetSubscribersRunUnlocked(t *testing.T) {
	s := NewStore(time.Now)
	calls := 0
	cancel := s.Subscribe(func(Snapshot) { _ = s.Current(); calls++ })
	_ = s.Report(context.Background(), Fields{Type: "none"}, "embed")
	_ = s.Report(context.Background(), Fields{Type: "none"}, "embed")
	cancel()
	_ = s.Report(context.Background(), Fields{Type: "wifi"}, "embed")
	if calls != 1 {
		t.Fatalf("calls=%d want 1", calls)
	}
}

func TestSubnetLateDetectionCannotUndoReportBinding(t *testing.T) {
	s := NewStore(time.Now)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	calls := 0
	s.SetDetector(func(context.Context) (Snapshot, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			return Snapshot{State: Online, Fields: Fields{Interface: "en0", Gateway: "10.1.1.1"}}, nil
		}
		return Snapshot{State: Online, Fields: Fields{Interface: "en0", Gateway: "192.168.1.1"}}, nil
	})
	go func() { _ = s.Detect(context.Background()); close(done) }()
	<-entered
	if err := s.Report(context.Background(), Fields{SSID: "HOME"}, "rest"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if s.Current().SSID != "HOME" || s.Current().Gateway != "192.168.1.1" || !s.View().Report.Valid {
		t.Fatalf("old probe rolled back new identity: %+v", s.View())
	}
}

func TestReviewK1FailedInlineDetectionBindsLastValidIdentity(t *testing.T) {
	now := time.Unix(100, 0)
	s := NewStore(func() time.Time { return now })
	network := Snapshot{State: Online, Fields: Fields{Interface: "en0", Gateway: "10.15.67.254"}}
	fail := false
	s.SetDetector(func(context.Context) (Snapshot, error) {
		if fail {
			return Snapshot{}, context.DeadlineExceeded
		}
		return network, nil
	})
	_ = s.Detect(context.Background())
	fail = true
	_ = s.Report(context.Background(), Fields{SSID: "GOLDWIND"}, "rest")
	fail = false
	network.Gateway = "192.168.1.1"
	now = now.Add(time.Minute)
	_ = s.Detect(context.Background())
	if s.View().Report.Valid || s.Current().SSID != "" {
		t.Fatalf("stale corporate report survived identity change: %+v", s.View())
	}
}

func TestReviewK1UnboundReportExpiresBeforeDistantFirstDetection(t *testing.T) {
	for _, age := range []time.Duration{3 * time.Second, 3*time.Second + time.Nanosecond} {
		t.Run(age.String(), func(t *testing.T) {
			now := time.Unix(100, 0)
			s := NewStore(func() time.Time { return now })
			fail := true
			s.SetDetector(func(context.Context) (Snapshot, error) {
				if fail {
					return Snapshot{}, context.Canceled
				}
				return Snapshot{State: Online, Fields: Fields{Interface: "en0", Gateway: "192.168.1.1"}}, nil
			})
			_ = s.Report(context.Background(), Fields{SSID: "GOLDWIND"}, "rest")
			now = now.Add(age)
			fail = false
			_ = s.Detect(context.Background())
			if got, want := s.View().Report.Valid, age <= 3*time.Second; got != want {
				t.Fatalf("report valid=%v after %v want %v", got, age, want)
			}
		})
	}
}
