package route

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"sync"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/log"

	"github.com/metacubex/http"
	"github.com/metacubex/tls"
)

// The HTTP server relinquishes hijacked WebSocket connections. Track sockets
// below TLS so retiring a controller can also revoke those subscriptions.
type forkControllerListener struct {
	net.Listener
	mu    sync.Mutex
	conns map[*forkControllerConn]struct{}
}

type forkControllerConn struct {
	net.Conn
	listener *forkControllerListener
}

type forkControllerConnKey struct{}

func (l *forkControllerListener) Accept() (net.Conn, error) {
	raw, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.conns == nil {
		_ = raw.Close()
		return nil, net.ErrClosed
	}
	c := &forkControllerConn{Conn: raw, listener: l}
	l.conns[c] = struct{}{}
	return c, nil
}

func (c *forkControllerConn) Close() error {
	c.listener.mu.Lock()
	delete(c.listener.conns, c)
	c.listener.mu.Unlock()
	return c.Conn.Close()
}

func (l *forkControllerListener) retire(keep *forkControllerConn) {
	_ = l.Close()
	l.mu.Lock()
	conns := l.conns
	l.conns = nil
	l.mu.Unlock()
	for c := range conns {
		if c != keep {
			_ = c.Conn.Close()
		}
	}
}

type forkController struct {
	listener *forkControllerListener
	server   *http.Server
	cancel   context.CancelFunc
}

var (
	forkLastServer Config
	forkLastUI     string
)

func (c *forkController) stop(keep *forkControllerConn) {
	if c.server == nil {
		return
	}
	c.cancel()
	if keep != nil && keep.listener == c.listener && c != &pipeServer {
		// Shutdown stops accepting requests and lets the HTTP server finish
		// framing the current response before it closes its connection.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = c.server.Shutdown(ctx)
	} else {
		keep = nil
		_ = c.server.Close()
	}
	c.listener.retire(keep)
	*c = forkController{}
}

type forkEndpoint struct {
	controller *forkController
	addr       string
	changed    bool
	listen     func(string, int) (net.Listener, error)
	secret     string
	suffix     string
}

func (e forkEndpoint) start(cfg *Config) error {
	if e.addr == "" || e.controller.server != nil {
		return nil
	}

	var tlsConfig *tls.Config
	var err error
	if e.controller == &tlsServer {
		tlsConfig, err = controllerTLSConfig(cfg)
		if err != nil {
			return err
		}
	}
	raw, err := e.listen(e.addr, cfg.RoutingMark)
	if err != nil {
		return err
	}
	clients := &forkControllerListener{Listener: raw, conns: make(map[*forkControllerConn]struct{})}
	var l net.Listener = clients
	if tlsConfig != nil {
		l = tls.NewListener(l, tlsConfig)
	}

	lifetime, cancel := context.WithCancel(context.Background())
	server := &http.Server{
		Handler:     router(cfg.IsDebug, e.secret, cfg.DohServer, cfg.Cors),
		BaseContext: func(net.Listener) context.Context { return lifetime },
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if tc, ok := conn.(http.TLSConn); ok {
				conn = tc.NetConn()
			}
			return context.WithValue(ctx, forkControllerConnKey{}, conn)
		},
	}
	*e.controller = forkController{listener: clients, server: server, cancel: cancel}
	log.Infoln("RESTful API%s listening at: %s", e.suffix, raw.Addr())
	go func() {
		if err := server.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Errorln("External controller%s serve error: %s", e.suffix, err)
		}
	}()
	return nil
}

func forkPrepareControllers(cfg *Config, keepUnchanged, reloadTLS bool, current *forkControllerConn) func() error {
	old := &forkLastServer
	routerChanged := !keepUnchanged || forkLastUI != uiPath || old.IsDebug != cfg.IsDebug ||
		old.DohServer != cfg.DohServer || !slices.Equal(old.Cors.AllowOrigins, cfg.Cors.AllowOrigins) ||
		old.Cors.AllowPrivateNetwork != cfg.Cors.AllowPrivateNetwork
	tcpChanged := routerChanged || old.Secret != cfg.Secret || old.RoutingMark != cfg.RoutingMark
	tlsChanged := reloadTLS || tcpChanged || old.Certificate != cfg.Certificate || old.PrivateKey != cfg.PrivateKey ||
		old.ClientAuthType != cfg.ClientAuthType || old.ClientAuthCert != cfg.ClientAuthCert || old.EchKey != cfg.EchKey
	endpoints := []forkEndpoint{
		{&httpServer, cfg.Addr, tcpChanged || old.Addr != cfg.Addr, listenTCP, cfg.Secret, ""},
		{&tlsServer, cfg.TLSAddr, tlsChanged || old.TLSAddr != cfg.TLSAddr, listenTCP, cfg.Secret, " tls"},
		{&unixServer, cfg.UnixAddr, routerChanged || old.UnixAddr != cfg.UnixAddr, listenUnix, "", " unix"},
	}
	if inbound.SupportNamedPipe {
		endpoints = append(endpoints, forkEndpoint{&pipeServer, cfg.PipeAddr, routerChanged || old.PipeAddr != cfg.PipeAddr, listenPipe, "", " pipe"})
	}

	// A controller can trade addresses with another controller, DNS or a proxy.
	for _, e := range endpoints {
		if e.changed {
			e.controller.stop(current)
		}
	}
	return func() error {
		var errs []error
		forkPublishGeneral()
		for _, e := range endpoints {
			if err := e.start(cfg); err != nil {
				log.Errorln("External controller%s listen error: %s", e.suffix, err)
				errs = append(errs, fmt.Errorf("failed to start external controller%s at %s", e.suffix, e.addr))
			}
		}
		forkLastServer = *cfg
		forkLastServer.Cors.AllowOrigins = slices.Clone(cfg.Cors.AllowOrigins)
		forkLastUI = uiPath
		return errors.Join(errs...)
	}
}
