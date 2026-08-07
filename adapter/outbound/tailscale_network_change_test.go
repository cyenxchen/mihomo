//go:build with_gvisor && !no_tailscale

package outbound

import "testing"

func TestNotifyTailscaleNetworkChangeInjectsStartedInstances(t *testing.T) {
	var injected int
	instance := &Tailscale{}
	instance.setNetworkChangeNotifier(func() {
		injected++
	})
	registerTailscaleInstance(instance)
	t.Cleanup(func() {
		unregisterTailscaleInstance(instance)
	})

	registered, notified := NotifyTailscaleNetworkChange("test0")
	if registered < 1 {
		t.Fatalf("registered instances = %d, want at least 1", registered)
	}
	if notified < 1 {
		t.Fatalf("notified instances = %d, want at least 1", notified)
	}
	if injected != 1 {
		t.Fatalf("injected events = %d, want 1", injected)
	}
}

func TestTailscaleNetworkChangeSkipsInstanceBeforeMonitorIsReady(t *testing.T) {
	instance := &Tailscale{}
	if instance.notifyNetworkChange() {
		t.Fatal("notifyNetworkChange = true without a ready netmon, want false")
	}
}
