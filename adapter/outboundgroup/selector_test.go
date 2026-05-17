package outboundgroup

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestSelector(t *testing.T, option *GroupCommonOption, proxies ...C.Proxy) *Selector {
	t.Helper()

	if option.ExpectedStatus != "" && option.expectedStatus == nil {
		expectedStatus, err := utils.NewUnsignedRanges[uint16](option.ExpectedStatus)
		require.NoError(t, err)
		option.expectedStatus = expectedStatus
	}

	hc := provider.NewHealthCheck(proxies, option.URL, 0, 0, true, option.expectedStatus)
	pd, err := provider.NewCompatibleProvider(option.Name+"-provider", proxies, hc)
	require.NoError(t, err)

	return NewSelector(option, []P.ProxyProvider{pd})
}

type directDialProxy struct {
	*stubProxy
}

func (d *directDialProxy) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", metadata.RemoteAddress())
	if err != nil {
		return nil, err
	}
	return outbound.NewConn(conn, d), nil
}

func TestSelectorUsesDefault(t *testing.T) {
	first := newStubProxy("First", "", 0, true)
	second := newStubProxy("Second", "", 0, true)

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "Second",
	}, first, second)

	assert.Equal(t, "Second", group.Now())
}

func TestSelectorExactDefaultDoesNotProbe(t *testing.T) {
	first := newStubProxy("proxy-node-1", "", 0, true)
	second := newStubProxy("proxy-node-2", "", 0, true)

	calls := 0
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, _ C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls++
		return 0, nil
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node-2",
	}, first, second)

	assert.Equal(t, "proxy-node-2", group.Now())
	assert.Zero(t, calls)
}

func TestSelectorDefaultPrefixDoesNotProbeBeforeSelection(t *testing.T) {
	first := newStubProxy("proxy-node-1", "", 0, true)
	second := newStubProxy("proxy-node-2", "", 0, true)

	calls := 0
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, _ C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls++
		return 0, nil
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node",
	}, first, second)
	group.ForceSet("proxy-node-2")

	assert.Equal(t, "proxy-node-2", group.Now())
	assert.Zero(t, calls)
}

func TestSelectorDefaultPrefixReadDoesNotProbeSynchronously(t *testing.T) {
	first := newStubProxy("proxy-node-1", "", 0, true)
	second := newStubProxy("proxy-node-2", "", 0, true)

	calls := 0
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, _ C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls++
		return 0, errors.New("should not probe from read path")
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node",
	}, first, second)

	assert.Equal(t, "proxy-node-1", group.Now())
	assert.Zero(t, calls)
}

func TestSelectorDefaultPrefixUsesFirstReachableCandidate(t *testing.T) {
	first := newStubProxy("proxy-node-1", "", 0, true)
	second := newStubProxy("proxy-node-2", "", 0, true)
	third := newStubProxy("proxy-node-3", "", 0, true)

	var calls []string
	var urls []string
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, proxy C.Proxy, rawURL string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls = append(calls, proxy.Name())
		urls = append(urls, rawURL)
		if proxy.Name() == "proxy-node-2" {
			return 1024, nil
		}
		return 0, errors.New("no speed")
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node",
		URL:     "https://example.test/download",
	}, first, second, third)
	group.StartDefaultSelection()

	require.Eventually(t, func() bool {
		return group.Now() == "proxy-node-2"
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"proxy-node-1", "proxy-node-2"}, calls)
	assert.Equal(t, []string{"https://example.test/download", "https://example.test/download"}, urls)
}

func TestSelectorDefaultWildcardUsesFirstReachableCandidate(t *testing.T) {
	first := newStubProxy("proxy-node-hk-1", "", 0, true)
	second := newStubProxy("proxy-node-us-1", "", 0, true)
	third := newStubProxy("proxy-node-hk-2", "", 0, true)

	calls := make(chan string, 2)
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, proxy C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls <- proxy.Name()
		if proxy.Name() == "proxy-node-hk-2" {
			return 1024, nil
		}
		return 0, errors.New("no speed")
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node-hk-*",
	}, first, second, third)
	group.StartDefaultSelection()

	require.Eventually(t, func() bool {
		return group.Now() == "proxy-node-hk-2"
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"proxy-node-hk-1", "proxy-node-hk-2"}, []string{<-calls, <-calls})
}

func TestSelectorDefaultWildcardQuestionMarkDoesNotUsePrefixSemantics(t *testing.T) {
	prefixOnly := newStubProxy("proxy?-literal", "", 0, true)
	matched := newStubProxy("proxy1", "", 0, true)
	unmatched := newStubProxy("proxy12", "", 0, true)

	calls := make(chan string, 1)
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, proxy C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls <- proxy.Name()
		return 1024, nil
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy?",
	}, prefixOnly, matched, unmatched)
	group.StartDefaultSelection()

	select {
	case call := <-calls:
		assert.Equal(t, "proxy1", call)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for default wildcard probe")
	}
	assert.Equal(t, "proxy1", group.Now())
}

func TestSelectorDefaultPrefixFallsBackToLastCandidate(t *testing.T) {
	first := newStubProxy("proxy-node-1", "", 0, true)
	second := newStubProxy("proxy-node-2", "", 0, true)
	third := newStubProxy("proxy-node-3", "", 0, true)

	var calls []string
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, proxy C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		calls = append(calls, proxy.Name())
		return 0, errors.New("no speed")
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node",
	}, first, second, third)
	group.StartDefaultSelection()

	require.Eventually(t, func() bool {
		return group.Now() == "proxy-node-3"
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"proxy-node-1", "proxy-node-2", "proxy-node-3"}, calls)
}

func TestSelectorDefaultSkipsNilProxy(t *testing.T) {
	var nilProxy C.Proxy
	first := newStubProxy("proxy-node-1", "", 0, true)

	done := make(chan struct{}, 1)
	oldProbe := selectorDefaultProbeDownload
	selectorDefaultProbeDownload = func(_ context.Context, proxy C.Proxy, _ string, _ utils.IntRanges[uint16], _ time.Duration) (uint64, error) {
		done <- struct{}{}
		return 1024, nil
	}
	defer func() {
		selectorDefaultProbeDownload = oldProbe
	}()

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "proxy-node",
	}, nilProxy, first)
	group.StartDefaultSelection()

	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, "proxy-node-1", group.Now())
}

func TestSelectorDefaultProbeTreatsFinite204ResponseAsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	proxy := &directDialProxy{stubProxy: newStubProxy("proxy-node-1", "", 0, true)}
	size, err := probeSelectorDefaultDownload(context.Background(), proxy, server.URL, nil, selectorDefaultProbeDuration)

	require.NoError(t, err)
	assert.Zero(t, size)
}

func TestSelectorDefaultPrefixHonorsExpectedStatus(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "https://example.test/ok")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(server.Close)

	first := &directDialProxy{stubProxy: newStubProxy("proxy-node-1", "", 0, true)}
	second := &directDialProxy{stubProxy: newStubProxy("proxy-node-2", "", 0, true)}
	third := &directDialProxy{stubProxy: newStubProxy("proxy-node-3", "", 0, true)}

	group := newTestSelector(t, &GroupCommonOption{
		Name:           "manual",
		Default:        "proxy-node",
		URL:            server.URL,
		ExpectedStatus: "302",
	}, first, second, third)
	group.StartDefaultSelection()

	require.Eventually(t, func() bool {
		return requests.Load() == 1
	}, time.Second, 10*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, "proxy-node-1", group.Now())
}

func TestSelectorDefaultFallsBackToFirstProxy(t *testing.T) {
	first := newStubProxy("First", "", 0, true)
	second := newStubProxy("Second", "", 0, true)

	group := newTestSelector(t, &GroupCommonOption{
		Name:    "manual",
		Default: "Missing",
	}, first, second)

	assert.Equal(t, "First", group.Now())
}
