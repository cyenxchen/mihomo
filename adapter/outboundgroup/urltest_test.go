package outboundgroup

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/metacubex/mihomo/adapter/provider"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubProxy struct {
	name   string
	alive  map[string]bool
	delays map[string]uint16
}

func newStubProxy(name, testURL string, delay uint16, alive bool) *stubProxy {
	return &stubProxy{
		name:   name,
		alive:  map[string]bool{testURL: alive},
		delays: map[string]uint16{testURL: delay},
	}
}

func (s *stubProxy) Name() string {
	return s.name
}

func (s *stubProxy) Type() C.AdapterType {
	return C.Http
}

func (s *stubProxy) Addr() string {
	return ""
}

func (s *stubProxy) SupportUDP() bool {
	return true
}

func (s *stubProxy) ProxyInfo() C.ProxyInfo {
	return C.ProxyInfo{}
}

func (s *stubProxy) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"name": s.name,
		"type": s.Type().String(),
	})
}

func (s *stubProxy) DialContext(_ context.Context, _ *C.Metadata) (C.Conn, error) {
	return nil, errors.New("not implemented")
}

func (s *stubProxy) ListenPacketContext(_ context.Context, _ *C.Metadata) (C.PacketConn, error) {
	return nil, errors.New("not implemented")
}

func (s *stubProxy) SupportUOT() bool {
	return false
}

func (s *stubProxy) IsL3Protocol(_ *C.Metadata) bool {
	return false
}

func (s *stubProxy) Unwrap(_ *C.Metadata, _ bool) C.Proxy {
	return nil
}

func (s *stubProxy) Close() error {
	return nil
}

func (s *stubProxy) Adapter() C.ProxyAdapter {
	return s
}

func (s *stubProxy) AliveForTestUrl(url string) bool {
	return s.alive[url]
}

func (s *stubProxy) DelayHistory() []C.DelayHistory {
	return nil
}

func (s *stubProxy) ExtraDelayHistories() map[string]C.ProxyState {
	return nil
}

func (s *stubProxy) LastDelayForTestUrl(url string) uint16 {
	if !s.AliveForTestUrl(url) {
		return 0xffff
	}
	return s.delays[url]
}

func (s *stubProxy) URLTest(_ context.Context, url string, _ utils.IntRanges[uint16]) (uint16, error) {
	if !s.AliveForTestUrl(url) {
		return 0, errors.New("proxy not alive")
	}
	return s.LastDelayForTestUrl(url), nil
}

func newTestURLTest(t *testing.T, option *GroupCommonOption, proxies ...C.Proxy) *URLTest {
	t.Helper()

	hc := provider.NewHealthCheck(proxies, option.URL, 0, 0, true, nil)
	pd, err := provider.NewCompatibleProvider(option.Name+"-provider", proxies, hc)
	require.NoError(t, err)

	opts, err := parseURLTestOption(option)
	require.NoError(t, err)

	return NewURLTest(option, []P.ProxyProvider{pd}, opts...)
}

func TestParseURLTestOptionPolicyPriority(t *testing.T) {
	option := &GroupCommonOption{
		Name:           "auto",
		URL:            "https://example.com",
		Tolerance:      150,
		PolicyPriority: "Premium:0.1;Hong Kong:0.2;",
	}

	opts, err := parseURLTestOption(option)
	require.NoError(t, err)

	group := NewURLTest(option, nil, opts...)
	require.Len(t, group.policyPriority, 2)
	assert.Equal(t, uint16(150), group.tolerance)
	assert.Equal(t, "Premium", group.policyPriority[0].pattern)
	assert.Equal(t, 0.1, group.policyPriority[0].factor)
	assert.Equal(t, "Hong Kong", group.policyPriority[1].pattern)
	assert.Equal(t, 0.2, group.policyPriority[1].factor)
}

func TestParseURLTestOptionInvalidPolicyPriority(t *testing.T) {
	testCases := []struct {
		name           string
		policyPriority string
		errContains    string
	}{
		{
			name:           "MissingFactor",
			policyPriority: "Premium",
			errContains:    "invalid policy-priority entry",
		},
		{
			name:           "InvalidRegex",
			policyPriority: "(:0.1",
			errContains:    "invalid policy-priority regex",
		},
		{
			name:           "InvalidFactor",
			policyPriority: "Premium:not-a-number",
			errContains:    "invalid policy-priority factor",
		},
		{
			name:           "NonPositiveFactor",
			policyPriority: "Premium:0",
			errContains:    "must be a finite number greater than 0",
		},
		{
			name:           "NaNFactor",
			policyPriority: "Premium:NaN",
			errContains:    "must be a finite number greater than 0",
		},
		{
			name:           "InfiniteFactor",
			policyPriority: "Premium:Inf",
			errContains:    "must be a finite number greater than 0",
		},
		{
			name:           "NegativeInfiniteFactor",
			policyPriority: "Premium:-Inf",
			errContains:    "must be a finite number greater than 0",
		},
		{
			name:           "RegexWithColonUsesLastSeparator",
			policyPriority: "(?:HK|TW):0.3",
			errContains:    "",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseURLTestOption(&GroupCommonOption{PolicyPriority: testCase.policyPriority})
			if testCase.errContains == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, testCase.errContains)
			}
		})
	}
}

func TestURLTestUsesWeightedDelay(t *testing.T) {
	testURL := "https://example.com"
	regular := newStubProxy("Regular", testURL, 50, true)
	premium := newStubProxy("Premium Node", testURL, 80, true)

	group := newTestURLTest(t, &GroupCommonOption{
		Name:           "auto",
		URL:            testURL,
		PolicyPriority: "Premium:0.1;",
	}, regular, premium)

	assert.Equal(t, "Premium Node", group.Now())
}

func TestURLTestUsesSmallestMatchingFactor(t *testing.T) {
	testURL := "https://example.com"
	regular := newStubProxy("Regular", testURL, 30, true)
	target := newStubProxy("Premium Hong Kong", testURL, 80, true)

	group := newTestURLTest(t, &GroupCommonOption{
		Name:           "auto",
		URL:            testURL,
		PolicyPriority: "Premium:0.9;Hong Kong:0.2;",
	}, regular, target)

	assert.Equal(t, "Premium Hong Kong", group.Now())
}

func TestURLTestToleranceUsesWeightedDelay(t *testing.T) {
	testURL := "https://example.com"
	current := newStubProxy("Current", testURL, 60, true)
	premium := newStubProxy("Premium Node", testURL, 80, true)

	group := newTestURLTest(t, &GroupCommonOption{
		Name:           "auto",
		URL:            testURL,
		Tolerance:      10,
		PolicyPriority: "Premium:0.7;",
	}, current, premium)
	group.fastNode = current
	group.fastSingle.Reset()

	assert.Equal(t, "Current", group.Now())
}

func TestURLTestSwitchesWhenWeightedDelayBeatsTolerance(t *testing.T) {
	testURL := "https://example.com"
	current := newStubProxy("Current", testURL, 60, true)
	premium := newStubProxy("Premium Node", testURL, 80, true)

	group := newTestURLTest(t, &GroupCommonOption{
		Name:           "auto",
		URL:            testURL,
		Tolerance:      10,
		PolicyPriority: "Premium:0.5;",
	}, current, premium)
	group.fastNode = current
	group.fastSingle.Reset()

	assert.Equal(t, "Premium Node", group.Now())
}

func TestURLTestDeadProxyDoesNotWinByWeight(t *testing.T) {
	testURL := "https://example.com"
	deadPremium := newStubProxy("Premium Dead", testURL, 80, false)
	regular := newStubProxy("Regular", testURL, 200, true)

	group := newTestURLTest(t, &GroupCommonOption{
		Name:           "auto",
		URL:            testURL,
		PolicyPriority: "Premium:0.1;",
	}, deadPremium, regular)

	assert.Equal(t, "Regular", group.Now())
}
