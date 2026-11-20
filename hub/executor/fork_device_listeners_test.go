package executor

import (
	"net"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/listener"
)

func TestDeviceListenersReportUnavailableMixedPort(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := &config.Config{General: &config.General{Inbound: config.Inbound{MixedPort: port}}, DNS: &config.DNS{}}
	t.Cleanup(func() { listener.ReCreateMixed(0, nil) })
	if err := ForkCheckDeviceListeners(cfg); err == nil || !strings.Contains(err.Error(), "mixed proxy") {
		t.Fatalf("occupied port: got %v", err)
	}
	l.Close()
	if err := ForkCheckDeviceListeners(cfg); err != nil {
		t.Fatal(err)
	}
	if got := listener.GetPorts().MixedPort; got != port {
		t.Fatalf("recovered mixed port = %d, want %d", got, port)
	}
}
