//go:build with_gvisor && !no_tailscale

package outbound

import "testing"

func TestTailscaleNodeNotFoundMarksRuntimeUnhealthy(t *testing.T) {
	instance, err := NewTailscale(TailscaleOption{
		Name:     "tailscale-control-health-test",
		StateDir: "tailscale-control-health-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		unregisterTailscaleInstance(instance)
	})

	health, ok := any(instance).(interface{ RuntimeAlive() bool })
	if !ok {
		t.Fatal("Tailscale adapter does not expose fatal runtime health")
	}
	if !health.RuntimeAlive() {
		t.Fatal("Tailscale adapter starts unhealthy")
	}

	instance.server.Logf("control: PollNetMap: initial fetch failed 404: node not found")
	if health.RuntimeAlive() {
		t.Fatal("Tailscale adapter remains alive after Headscale reports that its node is missing")
	}
}
