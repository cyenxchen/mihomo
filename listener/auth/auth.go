package auth

import (
	"github.com/metacubex/mihomo/common/atomic"
	"github.com/metacubex/mihomo/component/auth"
)

type authStore struct {
	authenticator atomic.TypedValue[auth.Authenticator]
}

func (a *authStore) Authenticator() auth.Authenticator {
	return a.authenticator.Load()
}

func (a *authStore) SetAuthenticator(authenticator auth.Authenticator) {
	a.authenticator.Store(authenticator)
}

func NewAuthStore(authenticator auth.Authenticator) auth.AuthStore {
	store := &authStore{}
	store.SetAuthenticator(authenticator)
	return store
}

var Default auth.AuthStore = NewAuthStore(nil)

type nilAuthStore struct{}

func (a *nilAuthStore) Authenticator() auth.Authenticator {
	return nil
}

func (a *nilAuthStore) SetAuthenticator(authenticator auth.Authenticator) {}

var Nil auth.AuthStore = (*nilAuthStore)(nil) // always return nil, even call SetAuthenticator() with a non-nil authenticator
