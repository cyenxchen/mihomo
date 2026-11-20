package listener

import (
	"net"
	"testing"
)

func TestDeviceReloadMixedUDPBindFailure(t *testing.T) {
	p, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	port := p.LocalAddr().(*net.UDPAddr).Port
	t.Cleanup(func() { ReCreateMixed(0, nil) })
	ReCreateMixed(port, nil)
	if n := GetPorts().MixedPort; n != 0 {
		t.Fatalf("mixed port reported %d after UDP bind failed", n)
	}
}
