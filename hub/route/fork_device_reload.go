package route

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/log"

	"github.com/metacubex/chi/render"
	"github.com/metacubex/http"
)

var (
	// Parsing temporarily changes live general settings before restoring them.
	// A second reload must wait through both parsing and application.
	forkReloadMu         sync.Mutex
	forkStartupOptions   func(*config.Config)
	forkRebindOnReload   bool
	forkPublishedGeneral atomic.Pointer[forkGeneralResponse]
)

type forkGeneralResponse struct {
	body []byte
	err  error
}

// A GET during reload can read the last published settings without observing
// temporary parser state or waiting for a network provider.
func forkPublishGeneral() *forkGeneralResponse {
	body, err := json.Marshal(executor.GetGeneral())
	view := &forkGeneralResponse{body: append(body, '\n'), err: err}
	forkPublishedGeneral.Store(view)
	return view
}

func ParseConfig(data []byte, options func(*config.Config)) error {
	forkReloadMu.Lock()
	defer forkReloadMu.Unlock()

	var cfg *config.Config
	var err error
	if len(data) == 0 {
		cfg, err = executor.Parse()
	} else {
		cfg, err = executor.ParseWithBytes(data)
	}
	if err != nil {
		return err
	}
	forkStartupOptions = options
	return forkApplyConfig(cfg, true, true, nil)
}

func ApplyConfig(cfg *config.Config) {
	forkReloadMu.Lock()
	defer forkReloadMu.Unlock()
	forkStartupOptions = nil
	forkApplyConfig(cfg, true, true, nil)
}

func forkApplyConfig(cfg *config.Config, force, full bool, current *forkControllerConn) error {
	rebind := forkRebindOnReload || cfg.HasDeviceOverrides
	defer forkPublishGeneral()
	if !full && !rebind {
		executor.ApplyConfigWithListeners(cfg, force, full, func() { forkPublishGeneral() })
		return nil
	}
	if forkStartupOptions != nil {
		forkStartupOptions(cfg)
	}
	if cfg.Controller.ExternalUI != "" {
		uiPath = C.Path.Resolve(cfg.Controller.ExternalUI)
	} else if rebind {
		uiPath = ""
	}
	startControllers := forkPrepareControllers(&Config{
		Addr:           cfg.Controller.ExternalController,
		TLSAddr:        cfg.Controller.ExternalControllerTLS,
		UnixAddr:       cfg.Controller.ExternalControllerUnix,
		PipeAddr:       cfg.Controller.ExternalControllerPipe,
		RoutingMark:    cfg.Controller.ExternalControllerRoutingMark,
		Secret:         cfg.Controller.Secret,
		Certificate:    cfg.TLS.Certificate,
		PrivateKey:     cfg.TLS.PrivateKey,
		ClientAuthType: cfg.TLS.ClientAuthType,
		ClientAuthCert: cfg.TLS.ClientAuthCert,
		EchKey:         cfg.TLS.EchKey,
		DohServer:      cfg.Controller.ExternalDohServer,
		IsDebug:        cfg.General.LogLevel == log.DEBUG,
		Cors: Cors{
			AllowOrigins:        cfg.Controller.Cors.AllowOrigins,
			AllowPrivateNetwork: cfg.Controller.Cors.AllowPrivateNetwork,
		},
	}, rebind, full, current)
	var err error
	executor.ApplyConfigWithListeners(cfg, force || rebind, full, func() {
		if rebind {
			err = executor.ForkCheckDeviceListeners(cfg)
		}
		err = errors.Join(err, startControllers())
	})
	if !rebind {
		return nil
	}
	// A failed bind while leaving a device profile still needs a retry.
	forkRebindOnReload = cfg.HasDeviceOverrides || err != nil
	if full && err != nil {
		log.Errorln("Configuration applied with listener errors: %v", err)
		return nil
	}
	return err
}

func forkLockConfigRequest(w http.ResponseWriter, r *http.Request) bool {
	forkReloadMu.Lock()
	server, _ := r.Context().Value(http.ServerContextKey).(*http.Server)
	if server != nil && server != httpServer.server && server != tlsServer.server && server != unixServer.server && server != pipeServer.server {
		forkReloadMu.Unlock()
		render.Status(r, http.StatusConflict)
		render.JSON(w, r, newError("controller was reloaded"))
		return false
	}
	return true
}
