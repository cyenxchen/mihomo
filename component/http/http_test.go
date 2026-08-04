package http

import (
	"context"
	stdHTTP "net/http"
	"net/http/httptest"
	"testing"

	"github.com/metacubex/mihomo/listener/inner"
)

func TestHttpRequestWithSpecialProxyDoesNotFallbackToDirect(t *testing.T) {
	// A configured provider proxy must fail closed while the tunnel is unavailable;
	// silently dialing the provider URL directly can leak traffic outside the VPN.
	originalTunnel := inner.GetTunnel()
	inner.New(nil)
	t.Cleanup(func() {
		inner.New(originalTunnel)
	})

	server := httptest.NewServer(stdHTTP.HandlerFunc(func(writer stdHTTP.ResponseWriter, _ *stdHTTP.Request) {
		writer.WriteHeader(stdHTTP.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	response, err := HttpRequest(
		context.Background(),
		server.URL,
		stdHTTP.MethodGet,
		nil,
		nil,
		WithSpecialProxy("provider-proxy"),
	)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("expected the request to fail instead of falling back to a direct connection")
	}
}
