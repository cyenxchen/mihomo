package dns

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/metacubex/mihomo/common/atomic"
	"github.com/metacubex/mihomo/common/sockopt"
	"github.com/metacubex/mihomo/component/resolver"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"

	D "github.com/miekg/dns"
)

var (
	serverMu   sync.Mutex
	address    string
	listenMark int
	server     = &Server{}

	dnsDefaultTTL uint32 = 600
)

type Server struct {
	service   atomic.TypedValue[resolver.Service]
	tcpServer *dnsEndpoint
	udpServer *dnsEndpoint
}

type serverHandler struct {
	*Server
	isUDP bool
}

// ServeDNS implement D.Handler ServeDNS
func (s serverHandler) ServeDNS(w D.ResponseWriter, r *D.Msg) {
	msg, err := s.service.Load().ServeMsg(context.Background(), r)
	if err != nil {
		m := new(D.Msg)
		m.SetRcode(r, D.RcodeServerFailure)
		// does not matter if this write fails
		w.WriteMsg(m)
		return
	}
	if s.isUDP {
		// RFC 6891: fit the reply into the client's advertised buffer size,
		// setting the TC bit if records must be dropped; 512 when no OPT present
		msg.Truncate(resolver.RequestUDPSize(r))
	}
	msg.Compress = true
	w.WriteMsg(msg)
}

func (s *Server) UDPHandler() D.Handler {
	return serverHandler{Server: s, isUDP: true}
}

func (s *Server) TCPHandler() D.Handler {
	return serverHandler{Server: s, isUDP: false}
}

func (s *Server) SetService(service resolver.Service) {
	s.service.Store(service)
}

func ReCreateServer(addr string, lc C.InboundListenConfig, service resolver.Service) {
	if err, ok := EnsureServer(addr, lc, service).(*net.AddrError); ok {
		log.Errorln("Start DNS server error: %s", err)
	}
}

// EnsureServer retains healthy sockets and retries any missing DNS transport.
func EnsureServer(addr string, lc C.InboundListenConfig, service resolver.Service) error {
	serverMu.Lock()
	defer serverMu.Unlock()

	mark := 0
	if marked, ok := lc.(interface{ RouteMark() int }); ok {
		mark = marked.RouteMark()
	}
	if addr != address || mark != listenMark || service == nil || lc == nil {
		server.tcpServer.stop()
		server.udpServer.stop()
		server = &Server{}
		address = ""
	}

	_, port, err := net.SplitHostPort(addr)
	if addr == "" || port == "0" || lc == nil || service == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if port == "" {
		return &net.AddrError{Err: "missing port in address", Addr: addr}
	}

	address, listenMark = addr, mark
	server.SetService(service)
	if !server.udpServer.running() {
		p, err := lc.ListenPacket(context.Background(), "udp", addr)
		if err != nil {
			log.Errorln("Start DNS server(UDP) error: %s", err)
		} else {
			if err := sockopt.UDPReuseaddr(p); err != nil {
				log.Warnln("Failed to Reuse UDP Address: %s", err)
			}
			server.udpServer = activateDNSListener(&D.Server{Addr: addr, PacketConn: p, Handler: server.UDPHandler()}, p, "UDP")
		}
	}
	if !server.tcpServer.running() {
		l, err := lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			log.Errorln("Start DNS server(TCP) error: %s", err)
		} else {
			server.tcpServer = activateDNSListener(&D.Server{Addr: addr, Listener: l, Handler: server.TCPHandler()}, l, "TCP")
		}
	}
	if !server.tcpServer.running() || !server.udpServer.running() {
		return fmt.Errorf("DNS listener at %s is incomplete (TCP=%t, UDP=%t)", addr, server.tcpServer.running(), server.udpServer.running())
	}
	return nil
}

type dnsEndpoint struct {
	server *D.Server
	done   chan struct{}
}

func (e *dnsEndpoint) running() bool {
	if e == nil {
		return false
	}
	select {
	case <-e.done:
		return false
	default:
		return true
	}
}

func (e *dnsEndpoint) stop() {
	if e == nil {
		return
	}
	// Retire both listening sockets even when an old DNS query is still in flight.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = e.server.ShutdownContext(ctx)
}

func activateDNSListener(srv *D.Server, socket io.Closer, network string) *dnsEndpoint {
	// Shutdown can only retire a socket after ActivateAndServe marks it started.
	ready := make(chan struct{})
	endpoint := &dnsEndpoint{server: srv, done: make(chan struct{})}
	srv.NotifyStartedFunc = func() { close(ready) }
	go func() {
		if err := srv.ActivateAndServe(); err != nil {
			_ = socket.Close()
			log.Errorln("DNS server(%s) error: %s", network, err)
		}
		close(endpoint.done)
	}()
	select {
	case <-ready:
		log.Infoln("DNS server(%s) listening at: %s", network, srv.Addr)
		return endpoint
	case <-endpoint.done:
		return nil
	}
}
