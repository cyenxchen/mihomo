package hub

import (
	"slices"

	"github.com/metacubex/mihomo/config"
	"github.com/metacubex/mihomo/hub/route"
)

type Option func(*config.Config)

func WithExternalUI(externalUI string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalUI = externalUI
	}
}

func WithExternalController(externalController string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalController = externalController
	}
}

func WithExternalControllerTLS(externalControllerTLS string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalControllerTLS = externalControllerTLS
	}
}

func WithExternalControllerUnix(externalControllerUnix string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalControllerUnix = externalControllerUnix
	}
}

func WithExternalControllerPipe(externalControllerPipe string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalControllerPipe = externalControllerPipe
	}
}

func WithExternalControllerRoutingMark(externalControllerRoutingMark int) Option {
	return func(cfg *config.Config) {
		cfg.Controller.ExternalControllerRoutingMark = externalControllerRoutingMark
	}
}

func WithSecret(secret string) Option {
	return func(cfg *config.Config) {
		cfg.Controller.Secret = secret
	}
}

// ApplyConfig dispatch configure to all parts include ExternalController
func ApplyConfig(cfg *config.Config) {
	route.ApplyConfig(cfg)
}

// Parse call at the beginning of mihomo
func Parse(configBytes []byte, options ...Option) error {
	options = slices.Clone(options)
	return route.ParseConfig(configBytes, func(cfg *config.Config) {
		for _, option := range options {
			option(cfg)
		}
	})
}
