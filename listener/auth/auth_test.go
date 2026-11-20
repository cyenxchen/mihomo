package auth

import (
	"testing"

	"github.com/metacubex/mihomo/component/auth"
)

func TestAuthenticatorDuringReload(t *testing.T) {
	before := auth.NewAuthenticator([]auth.AuthUser{{User: "old", Pass: "test"}})
	after := auth.NewAuthenticator([]auth.AuthUser{{User: "new", Pass: "test"}})
	store := NewAuthStore(before)
	read := make(chan auth.Authenticator)
	go func() { read <- store.Authenticator() }()
	store.SetAuthenticator(after)
	if got := <-read; got != before && got != after {
		t.Fatal("connection did not receive a complete authenticator")
	}
	if store.Authenticator() != after {
		t.Fatal("new connections must see the reloaded authenticator")
	}
	store.SetAuthenticator(nil)
	if store.Authenticator() != nil {
		t.Fatal("clearing authentication must preserve nil")
	}
}
