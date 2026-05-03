package config

import (
	"testing"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateDialerProxies(t *testing.T) {
	testCases := []struct {
		testName    string
		proxy       []map[string]any
		errContains string
	}{
		{
			testName: "ValidReference",
			proxy: []map[string]any{ // create proxy with valid dialer-proxy reference
				{"name": "base-proxy", "type": "socks5", "server": "127.0.0.1", "port": 1080},
				{"name": "proxy-with-dialer", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "base-proxy"},
			},
			errContains: "",
		},
		{
			testName: "NotFoundReference",
			proxy: []map[string]any{ // create proxy with non-existent dialer-proxy reference
				{"name": "proxy-with-dialer", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "non-existent-proxy"},
			},
			errContains: "not found",
		},
		{
			testName: "CircularDependency",
			proxy: []map[string]any{
				// create proxy A that references B
				{"name": "proxy-a", "type": "socks5", "server": "127.0.0.1", "port": 1080, "dialer-proxy": "proxy-c"},
				// create proxy B that references C
				{"name": "proxy-b", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "proxy-a"},
				// create proxy C that references A (creates cycle)
				{"name": "proxy-c", "type": "socks5", "server": "127.0.0.1", "port": 1082, "dialer-proxy": "proxy-a"},
			},
			errContains: "circular",
		},
		{
			testName: "ComplexChain",
			proxy: []map[string]any{ // create a valid chain: proxy-d -> proxy-c -> proxy-b -> proxy-a
				{"name": "proxy-a", "type": "socks5", "server": "127.0.0.1", "port": 1080},
				{"name": "proxy-b", "type": "socks5", "server": "127.0.0.1", "port": 1081, "dialer-proxy": "proxy-a"},
				{"name": "proxy-c", "type": "socks5", "server": "127.0.0.1", "port": 1082, "dialer-proxy": "proxy-b"},
				{"name": "proxy-d", "type": "socks5", "server": "127.0.0.1", "port": 1083, "dialer-proxy": "proxy-c"},
			},
			errContains: "",
		},
		{
			testName: "EmptyDialerProxy",
			proxy: []map[string]any{ // create proxy without dialer-proxy
				{"name": "simple-proxy", "type": "socks5", "server": "127.0.0.1", "port": 1080},
			},
			errContains: "",
		},
		{
			testName: "SelfReference",
			proxy: []map[string]any{ // create proxy that references itself
				{"name": "self-proxy", "type": "socks5", "server": "127.0.0.1", "port": 1080, "dialer-proxy": "self-proxy"},
			},
			errContains: "circular",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.testName, func(t *testing.T) {
			config := RawConfig{Proxy: testCase.proxy}
			_, _, err := parseProxies(&config)
			if testCase.errContains == "" {
				assert.NoError(t, err, testCase.testName)
			} else {
				assert.ErrorContains(t, err, testCase.errContains, testCase.testName)
			}
		})
	}
}

func TestParseProxiesPolicyPriority(t *testing.T) {
	testCases := []struct {
		testName    string
		group       []map[string]any
		errContains string
	}{
		{
			testName: "ValidURLTestPolicyPriority",
			group: []map[string]any{
				{
					"name":            "auto",
					"type":            "url-test",
					"proxies":         []string{"test-proxy"},
					"policy-priority": "Premium:0.1;Hong Kong:0.2;",
				},
			},
		},
		{
			testName: "InvalidPolicyPriorityType",
			group: []map[string]any{
				{
					"name":            "manual",
					"type":            "select",
					"proxies":         []string{"test-proxy"},
					"policy-priority": "Premium:0.1;",
				},
			},
			errContains: "policy-priority only supports url-test",
		},
		{
			testName: "InvalidPolicyPriorityFactor",
			group: []map[string]any{
				{
					"name":            "auto",
					"type":            "url-test",
					"proxies":         []string{"test-proxy"},
					"policy-priority": "Premium:0;",
				},
			},
			errContains: "must be a finite number greater than 0",
		},
		{
			testName: "InvalidPolicyPriorityRegex",
			group: []map[string]any{
				{
					"name":            "auto",
					"type":            "url-test",
					"proxies":         []string{"test-proxy"},
					"policy-priority": "(:0.1;",
				},
			},
			errContains: "invalid policy-priority regex",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.testName, func(t *testing.T) {
			config := RawConfig{
				Proxy: []map[string]any{
					{"name": "test-proxy", "type": "socks5", "server": "127.0.0.1", "port": 1080},
				},
				ProxyGroup: testCase.group,
			}
			_, _, err := parseProxies(&config)
			if testCase.errContains == "" {
				assert.NoError(t, err, testCase.testName)
			} else {
				assert.ErrorContains(t, err, testCase.errContains, testCase.testName)
			}
		})
	}
}

func TestParseProxiesSelectDefault(t *testing.T) {
	config := RawConfig{
		Proxy: []map[string]any{
			{"name": "proxy-a", "type": "socks5", "server": "127.0.0.1", "port": 1080},
			{"name": "proxy-b", "type": "socks5", "server": "127.0.0.1", "port": 1081},
		},
		ProxyGroup: []map[string]any{
			{
				"name":    "manual",
				"type":    "select",
				"default": "proxy-b",
				"proxies": []string{"proxy-a", "proxy-b"},
			},
		},
	}

	proxies, _, err := parseProxies(&config)
	require.NoError(t, err)

	selector, ok := proxies["manual"].Adapter().(*outboundgroup.Selector)
	require.True(t, ok)
	assert.Equal(t, "proxy-b", selector.Now())
}

func TestParseProxiesSelectDefaultValidation(t *testing.T) {
	testCases := []struct {
		testName    string
		group       map[string]any
		errContains string
	}{
		{
			testName: "DefaultProxyNotFound",
			group: map[string]any{
				"name":    "manual",
				"type":    "select",
				"default": "proxy-c",
				"proxies": []string{"proxy-a", "proxy-b"},
			},
			errContains: "default proxy or prefix 'proxy-c' not found",
		},
		{
			testName: "DefaultOnlySupportsSelect",
			group: map[string]any{
				"name":    "auto",
				"type":    "url-test",
				"default": "proxy-a",
				"proxies": []string{"proxy-a", "proxy-b"},
			},
			errContains: "default only supports select",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.testName, func(t *testing.T) {
			config := RawConfig{
				Proxy: []map[string]any{
					{"name": "proxy-a", "type": "socks5", "server": "127.0.0.1", "port": 1080},
					{"name": "proxy-b", "type": "socks5", "server": "127.0.0.1", "port": 1081},
				},
				ProxyGroup: []map[string]any{testCase.group},
			}

			_, _, err := parseProxies(&config)
			assert.ErrorContains(t, err, testCase.errContains)
		})
	}
}
