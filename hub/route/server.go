package route

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/adapter/inbound"
	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/component/ech"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/ntp"
	"github.com/metacubex/mihomo/tunnel/statistic"

	"github.com/metacubex/chi"
	"github.com/metacubex/chi/cors"
	"github.com/metacubex/chi/middleware"
	"github.com/metacubex/chi/render"
	"github.com/metacubex/http"
	"github.com/metacubex/tls"
)

var (
	uiPath = ""

	httpServer forkController
	tlsServer  forkController
	unixServer forkController
	pipeServer forkController

	embedMode = false
)

func SetEmbedMode(embed bool) {
	embedMode = embed
}

type Traffic struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	UpTotal   int64 `json:"upTotal"`
	DownTotal int64 `json:"downTotal"`
}

type Memory struct {
	Inuse   uint64 `json:"inuse"`
	OSLimit uint64 `json:"oslimit"` // maybe we need it in the future
}

type Config struct {
	Addr           string
	TLSAddr        string
	UnixAddr       string
	PipeAddr       string
	RoutingMark    int
	Secret         string
	Certificate    string
	PrivateKey     string
	ClientAuthType string
	ClientAuthCert string
	EchKey         string
	DohServer      string
	IsDebug        bool
	Cors           Cors
}

type Cors struct {
	AllowOrigins        []string
	AllowPrivateNetwork bool
}

func (c Cors) Apply(r chi.Router) {
	r.Use(cors.New(cors.Options{
		AllowedOrigins:      c.AllowOrigins,
		AllowedMethods:      []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
		AllowedHeaders:      []string{"Content-Type", "Authorization"},
		AllowPrivateNetwork: c.AllowPrivateNetwork,
		MaxAge:              300,
	}).Handler)
}

func ReCreateServer(cfg *Config) {
	forkReloadMu.Lock()
	defer forkReloadMu.Unlock()
	start := forkPrepareControllers(cfg, false, true, nil)
	_ = start()
}

func SetUIPath(path string) {
	forkReloadMu.Lock()
	defer forkReloadMu.Unlock()
	uiPath = C.Path.Resolve(path)
}

func router(isDebug bool, secret string, dohServer string, cors Cors) *chi.Mux {
	r := chi.NewRouter()
	cors.Apply(r)
	if isDebug {
		r.Mount("/debug", func() http.Handler {
			r := chi.NewRouter()
			r.Put("/gc", func(w http.ResponseWriter, r *http.Request) {
				debug.FreeOSMemory()
			})
			handler := middleware.Profiler
			r.Mount("/", handler())
			return r
		}())
	}
	r.Group(func(r chi.Router) {
		if secret != "" {
			r.Use(authentication(secret))
		}
		r.Use(forkNetworkAuthentication(secret))
		r.Get("/", hello)
		r.Get("/logs", getLogs)
		r.Get("/traffic", traffic)
		r.Get("/memory", memory)
		r.Get("/version", version)
		r.Mount("/configs", configRouter())
		r.Mount("/proxies", proxyRouter())
		r.Mount("/group", groupRouter())
		r.Mount("/rules", ruleRouter())
		r.Mount("/connections", connectionRouter())
		r.Mount("/providers/proxies", proxyProviderRouter())
		r.Mount("/providers/rules", ruleProviderRouter())
		r.Mount("/cache", cacheRouter())
		r.Mount("/dns", dnsRouter())
		r.Mount("/storage", storageRouter())
		if !embedMode { // disallow restart in embed mode
			r.Mount("/restart", restartRouter())
		}
		r.Mount("/upgrade", upgradeRouter())
		addExternalRouters(r)

	})

	if uiPath != "" {
		r.Group(func(r chi.Router) {
			fs := http.StripPrefix("/ui", http.FileServer(http.Dir(uiPath)))
			r.Get("/ui", http.RedirectHandler("/ui/", http.StatusTemporaryRedirect).ServeHTTP)
			r.Get("/ui/*", func(w http.ResponseWriter, r *http.Request) {
				fs.ServeHTTP(w, r)
			})
		})
	}
	if len(dohServer) > 0 && dohServer[0] == '/' {
		r.Mount(dohServer, dohRouter())
	}

	return r
}

func listenTCP(addr string, mark int) (net.Listener, error) {
	lc := inbound.NewListenConfig()
	lc.SetRouteMark(mark)
	return lc.Listen(context.Background(), "tcp", addr)
}

func controllerTLSConfig(cfg *Config) (*tls.Config, error) {
	certLoader, err := ca.NewTLSKeyPairLoader(cfg.Certificate, cfg.PrivateKey)
	if err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{Time: ntp.Now}
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	tlsConfig.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return certLoader()
	}
	tlsConfig.ClientAuth = ca.ClientAuthTypeFromString(cfg.ClientAuthType)
	if len(cfg.ClientAuthCert) > 0 && tlsConfig.ClientAuth == tls.NoClientCert {
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	if tlsConfig.ClientAuth == tls.VerifyClientCertIfGiven || tlsConfig.ClientAuth == tls.RequireAndVerifyClientCert {
		pool, err := ca.LoadCertificates(cfg.ClientAuthCert)
		if err != nil {
			return nil, err
		}
		tlsConfig.ClientCAs = pool
	}
	if cfg.EchKey != "" {
		if err := ech.LoadECHKey(cfg.EchKey, tlsConfig); err != nil {
			return nil, err
		}
	}
	return tlsConfig, nil
}

func listenUnix(path string, _ int) (net.Listener, error) {
	addr := C.Path.Resolve(path)
	dir := filepath.Dir(addr)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	// Windows also leaves the Unix socket file behind after closing its listener.
	_ = syscall.Unlink(addr)
	lc := inbound.NewListenConfig()
	lc.SetRouteMark(0) // don't set route mark for unix socket
	l, err := lc.Listen(context.Background(), "unix", addr)
	if err == nil {
		_ = os.Chmod(addr, 0o666)
	}
	return l, err
}

func listenPipe(addr string, _ int) (net.Listener, error) {
	if !strings.HasPrefix(addr, `\\.\pipe\`) {
		return nil, fmt.Errorf("windows namedpipe must start with %q", `\\.\pipe\`)
	}
	return inbound.ListenNamedPipe(addr)
}

func safeEqual(a, b string) bool {
	aBuf := utils.ImmutableBytesFromString(a)
	bBuf := utils.ImmutableBytesFromString(b)
	return subtle.ConstantTimeCompare(aBuf, bBuf) == 1
}

func authentication(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			// Browser websocket not support custom header
			if r.Header.Get("Upgrade") == "websocket" && r.URL.Query().Get("token") != "" {
				token := r.URL.Query().Get("token")
				if !safeEqual(token, secret) {
					render.Status(r, http.StatusUnauthorized)
					render.JSON(w, r, ErrUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			header := r.Header.Get("Authorization")
			bearer, token, found := strings.Cut(header, " ")

			hasInvalidHeader := bearer != "Bearer"
			hasInvalidSecret := !found || !safeEqual(token, secret)
			if hasInvalidHeader || hasInvalidSecret {
				render.Status(r, http.StatusUnauthorized)
				render.JSON(w, r, ErrUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		}
		return http.HandlerFunc(fn)
	}
}

func hello(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, render.M{"hello": "mihomo"})
}

func traffic(w http.ResponseWriter, r *http.Request) {
	var wsConn net.Conn
	if r.Header.Get("Upgrade") == "websocket" {
		var err error
		wsConn, _, err = wsUpgrade(r, w)
		if err != nil {
			return
		}
		defer wsConn.Close()
	}

	if wsConn == nil {
		w.Header().Set("Content-Type", "application/json")
		render.Status(r, http.StatusOK)
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	t := statistic.DefaultManager
	buf := &bytes.Buffer{}
	var err error
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		buf.Reset()
		up, down := t.Now()
		upTotal, downTotal := t.Total()
		if err := json.NewEncoder(buf).Encode(Traffic{
			Up:        up,
			Down:      down,
			UpTotal:   upTotal,
			DownTotal: downTotal,
		}); err != nil {
			break
		}

		if wsConn == nil {
			_, err = w.Write(buf.Bytes())
			w.(http.Flusher).Flush()
		} else {
			err = wsWriteServerText(wsConn, buf.Bytes())
		}

		if err != nil {
			break
		}
	}
}

func memory(w http.ResponseWriter, r *http.Request) {
	var wsConn net.Conn
	if r.Header.Get("Upgrade") == "websocket" {
		var err error
		wsConn, _, err = wsUpgrade(r, w)
		if err != nil {
			return
		}
		defer wsConn.Close()
	}

	if wsConn == nil {
		w.Header().Set("Content-Type", "application/json")
		render.Status(r, http.StatusOK)
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	t := statistic.DefaultManager
	buf := &bytes.Buffer{}
	var err error
	first := true
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
		buf.Reset()

		inuse := t.Memory()
		// make chat.js begin with zero
		// this is shit var,but we need output 0 for first time
		if first {
			inuse = 0
			first = false
		}
		if err := json.NewEncoder(buf).Encode(Memory{
			Inuse:   inuse,
			OSLimit: 0,
		}); err != nil {
			break
		}
		if wsConn == nil {
			_, err = w.Write(buf.Bytes())
			w.(http.Flusher).Flush()
		} else {
			err = wsWriteServerText(wsConn, buf.Bytes())
		}

		if err != nil {
			break
		}
	}
}

type Log struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}
type LogStructuredField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type LogStructured struct {
	Time    string               `json:"time"`
	Level   string               `json:"level"`
	Message string               `json:"message"`
	Fields  []LogStructuredField `json:"fields"`
}

func getLogs(w http.ResponseWriter, r *http.Request) {
	levelText := r.URL.Query().Get("level")
	if levelText == "" {
		levelText = "info"
	}

	formatText := r.URL.Query().Get("format")
	isStructured := false
	if formatText == "structured" {
		isStructured = true
	}

	level, ok := log.LogLevelMapping[levelText]
	if !ok {
		render.Status(r, http.StatusBadRequest)
		render.JSON(w, r, ErrBadRequest)
		return
	}

	var wsConn net.Conn
	if r.Header.Get("Upgrade") == "websocket" {
		var err error
		wsConn, _, err = wsUpgrade(r, w)
		if err != nil {
			return
		}
		defer wsConn.Close()
	}

	if wsConn == nil {
		w.Header().Set("Content-Type", "application/json")
		render.Status(r, http.StatusOK)
	}

	ch := make(chan log.Event, 1024)
	sub := log.Subscribe()
	defer log.UnSubscribe(sub)
	buf := &bytes.Buffer{}

	go func() {
		for logM := range sub {
			select {
			case ch <- logM:
			default:
			}
		}
		close(ch)
	}()

	for {
		var logM log.Event
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			logM = event
		}
		if logM.LogLevel < level {
			continue
		}
		buf.Reset()

		if !isStructured {
			if err := json.NewEncoder(buf).Encode(Log{
				Type:    logM.Type(),
				Payload: logM.Payload,
			}); err != nil {
				break
			}
		} else {
			newLevel := logM.Type()
			if newLevel == "warning" {
				newLevel = "warn"
			}
			if err := json.NewEncoder(buf).Encode(LogStructured{
				Time:    time.Now().Format(time.TimeOnly),
				Level:   newLevel,
				Message: logM.Payload,
				Fields:  []LogStructuredField{},
			}); err != nil {
				break
			}
		}

		var err error
		if wsConn == nil {
			_, err = w.Write(buf.Bytes())
			w.(http.Flusher).Flush()
		} else {
			err = wsWriteServerText(wsConn, buf.Bytes())
		}

		if err != nil {
			break
		}
	}
}

func version(w http.ResponseWriter, r *http.Request) {
	render.JSON(w, r, render.M{"meta": C.Meta, "version": C.Version})
}
