package dns

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/log"

	D "github.com/miekg/dns"
)

type retryDNSListener struct {
	net.ListenConfig
	fail      atomic.Bool
	attempted chan string
}

func (l *retryDNSListener) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	if l.fail.Load() {
		l.attempted <- network
		return nil, errors.New("test bind failure")
	}
	return l.ListenConfig.Listen(ctx, network, addr)
}

func (l *retryDNSListener) ListenPacket(ctx context.Context, network, addr string) (net.PacketConn, error) {
	if l.fail.Load() {
		l.attempted <- network
		return nil, errors.New("test bind failure")
	}
	return l.ListenConfig.ListenPacket(ctx, network, addr)
}

type reloadDNSReply struct{}

func (reloadDNSReply) ServeMsg(_ context.Context, q *D.Msg) (*D.Msg, error) {
	return new(D.Msg).SetReply(q), nil
}

func TestReCreateServerLogsInvalidDNSListenAddress(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want []string
	}{
		{
			name: "missing port",
			addr: "127.0.0.1",
			want: []string{"Start DNS server error: address 127.0.0.1: missing port in address"},
		},
		{
			name: "empty port",
			addr: "127.0.0.1:",
			want: []string{"Start DNS server error: address 127.0.0.1:: missing port in address"},
		},
		{name: "empty listen"},
		{name: "zero port", addr: "127.0.0.1:0"},
		{
			name: "bind errors",
			addr: "127.0.0.1:53",
			want: []string{
				"Start DNS server(TCP) error: test bind failure",
				"Start DNS server(UDP) error: test bind failure",
			},
		},
	}
	t.Cleanup(func() { ReCreateServer("", nil, nil) })
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := log.Subscribe()
			defer log.UnSubscribe(sub)
			lc := &retryDNSListener{attempted: make(chan string, 2)}
			lc.fail.Store(true)
			ReCreateServer(tt.addr, lc, reloadDNSReply{})
			marker := t.Name() + " completed"
			log.Debugln("%s", marker)
			var got []string
			for event := range sub {
				if event.Payload == marker {
					break
				}
				if event.LogLevel == log.ERROR && strings.HasPrefix(event.Payload, "Start DNS server") {
					got = append(got, event.Payload)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("DNS startup errors for listen %q = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestDeviceReloadRetriesFailedDNSBind(t *testing.T) {
	addr := unusedDNSAddress(t)
	t.Cleanup(func() { ReCreateServer("", nil, nil) })
	lc := &retryDNSListener{attempted: make(chan string, 2)}
	lc.fail.Store(true)
	ReCreateServer(addr, lc, reloadDNSReply{})
	for i := 0; i < 2; i++ {
		select {
		case <-lc.attempted:
		case <-time.After(2 * time.Second):
			t.Fatal("initial bind missing")
		}
	}
	lc.fail.Store(false)
	ReCreateServer(addr, lc, reloadDNSReply{})
	for _, network := range []string{"tcp", "udp"} {
		c := &D.Client{Net: network, Timeout: time.Second}
		q := new(D.Msg).SetQuestion("test.invalid.", D.TypeA)
		if _, _, err := c.Exchange(q, addr); err != nil {
			t.Errorf("%s after retry: %v", network, err)
		}
	}
}

type blockingDNSReply struct {
	received chan struct{}
	release  chan struct{}
}

func (s blockingDNSReply) ServeMsg(_ context.Context, q *D.Msg) (*D.Msg, error) {
	close(s.received)
	<-s.release
	return new(D.Msg).SetReply(q), nil
}

func unusedDNSAddress(t *testing.T) string {
	t.Helper()
	for i := 0; i < 20; i++ {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		p, err := net.ListenPacket("udp4", addr)
		l.Close()
		if err == nil {
			p.Close()
			return addr
		}
	}
	t.Fatal("no port available for TCP and UDP")
	return ""
}

func TestDeviceReloadDNSRetiresWithQueryInFlight(t *testing.T) {
	addr := unusedDNSAddress(t)
	s := blockingDNSReply{make(chan struct{}), make(chan struct{})}
	if err := EnsureServer(addr, &net.ListenConfig{}, s); err != nil {
		t.Fatal(err)
	}
	defer close(s.release)
	q := new(D.Msg).SetQuestion("test.invalid.", D.TypeA)
	go func() { _, _, _ = (&D.Client{Net: "tcp", Timeout: 2 * time.Second}).Exchange(q, addr) }()
	select {
	case <-s.received:
	case <-time.After(2 * time.Second):
		t.Fatal("missing local query")
	}
	done := make(chan struct{})
	go func() {
		ReCreateServer("", nil, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retired listener waited for its in-flight DNS query")
	}
}

type reopenDNSListener struct {
	net.ListenConfig
	mark    int
	last    net.Listener
	listens int
}

func (l *reopenDNSListener) RouteMark() int { return l.mark }

func (l *reopenDNSListener) Listen(ctx context.Context, network, addr string) (net.Listener, error) {
	conn, err := l.ListenConfig.Listen(ctx, network, addr)
	if err == nil {
		l.last = conn
		l.listens++
	}
	return conn, err
}

func TestDeviceReloadDNSReopensSockets(t *testing.T) {
	for _, change := range []string{"routing mark", "stopped TCP listener"} {
		t.Run(change, func(t *testing.T) {
			addr := unusedDNSAddress(t)
			l := &reopenDNSListener{}
			t.Cleanup(func() { ReCreateServer("", nil, nil) })
			if err := EnsureServer(addr, l, reloadDNSReply{}); err != nil {
				t.Fatal(err)
			}
			if change == "routing mark" {
				l.mark = 1
			} else {
				l.last.Close()
			}
			until := time.Now().Add(time.Second)
			for l.listens == 1 && time.Now().Before(until) {
				if err := EnsureServer(addr, l, reloadDNSReply{}); err != nil {
					t.Fatal(err)
				}
				if l.listens == 1 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if l.listens != 2 {
				t.Fatalf("TCP listener was not recreated after %s", change)
			}
		})
	}
}
