package outboundgroup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/metacubex/mihomo/common/callback"
	N "github.com/metacubex/mihomo/common/net"
	"github.com/metacubex/mihomo/common/singledo"
	"github.com/metacubex/mihomo/common/utils"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
)

type urlTestOption func(*URLTest)

func urlTestWithTolerance(tolerance uint16) urlTestOption {
	return func(u *URLTest) {
		u.tolerance = tolerance
	}
}

func urlTestWithPolicyPriority(policyPriority []policyPriorityRule) urlTestOption {
	return func(u *URLTest) {
		u.policyPriority = policyPriority
	}
}

type policyPriorityRule struct {
	pattern string
	regex   *regexp2.Regexp
	factor  float64
}

type URLTest struct {
	*GroupBase
	selected       string
	testUrl        string
	expectedStatus string
	tolerance      uint16
	policyPriority []policyPriorityRule
	disableUDP     bool
	Hidden         bool
	Icon           string
	fastNode       C.Proxy
	fastSingle     *singledo.Single[C.Proxy]
}

func (u *URLTest) Now() string {
	return u.fast(false).Name()
}

func (u *URLTest) Set(name string) error {
	var p C.Proxy
	for _, proxy := range u.GetProxies(false) {
		if proxy.Name() == name {
			p = proxy
			break
		}
	}
	if p == nil {
		return errors.New("proxy not exist")
	}
	u.ForceSet(name)
	return nil
}

func (u *URLTest) ForceSet(name string) {
	u.selected = name
	u.fastSingle.Reset()
}

// DialContext implements C.ProxyAdapter
func (u *URLTest) DialContext(ctx context.Context, metadata *C.Metadata) (c C.Conn, err error) {
	proxy := u.fast(true)
	c, err = proxy.DialContext(ctx, metadata)
	if err == nil {
		c.AppendToChains(u)
	} else {
		u.onDialFailed(proxy.Type(), err, u.healthCheck)
	}

	if N.NeedHandshake(c) {
		c = callback.NewFirstWriteCallBackConn(c, func(err error) {
			if err == nil {
				u.onDialSuccess()
			} else {
				u.onDialFailed(proxy.Type(), err, u.healthCheck)
			}
		})
	}

	return c, err
}

// ListenPacketContext implements C.ProxyAdapter
func (u *URLTest) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	proxy := u.fast(true)
	pc, err := proxy.ListenPacketContext(ctx, metadata)
	if err == nil {
		pc.AppendToChains(u)
	} else {
		u.onDialFailed(proxy.Type(), err, u.healthCheck)
	}

	return pc, err
}

// Unwrap implements C.ProxyAdapter
func (u *URLTest) Unwrap(metadata *C.Metadata, touch bool) C.Proxy {
	return u.fast(touch)
}

func (u *URLTest) healthCheck() {
	u.fastSingle.Reset()
	u.GroupBase.healthCheck()
	u.fastSingle.Reset()
}

func (u *URLTest) fast(touch bool) C.Proxy {
	elm, _, shared := u.fastSingle.Do(func() (C.Proxy, error) {
		proxies := u.GetProxies(touch)
		if u.selected != "" {
			for _, proxy := range proxies {
				if !proxy.AliveForTestUrl(u.testUrl) {
					continue
				}
				if proxy.Name() == u.selected {
					u.fastNode = proxy
					return proxy, nil
				}
			}
		}

		fast := proxies[0]
		minDelay := u.delayForComparison(fast)
		fastNotExist := u.fastNode != nil && fast.Name() != u.fastNode.Name()

		for _, proxy := range proxies[1:] {
			if u.fastNode != nil && proxy.Name() == u.fastNode.Name() {
				fastNotExist = false
			}

			delay := u.delayForComparison(proxy)
			if delay < minDelay {
				fast = proxy
				minDelay = delay
			}

		}
		// tolerance
		if u.fastNode == nil || fastNotExist || !u.fastNode.AliveForTestUrl(u.testUrl) || u.delayForComparison(u.fastNode) > minDelay+float64(u.tolerance) {
			u.fastNode = fast
		}
		return u.fastNode, nil
	})
	if shared && touch { // a shared fastSingle.Do() may cause providers untouched, so we touch them again
		u.Touch()
	}

	return elm
}

// SupportUDP implements C.ProxyAdapter
func (u *URLTest) SupportUDP() bool {
	if u.disableUDP {
		return false
	}
	return u.fast(false).SupportUDP()
}

// IsL3Protocol implements C.ProxyAdapter
func (u *URLTest) IsL3Protocol(metadata *C.Metadata) bool {
	return u.fast(false).IsL3Protocol(metadata)
}

// MarshalJSON implements C.ProxyAdapter
func (u *URLTest) MarshalJSON() ([]byte, error) {
	all := []string{}
	for _, proxy := range u.GetProxies(false) {
		all = append(all, proxy.Name())
	}
	return json.Marshal(map[string]any{
		"type":           u.Type().String(),
		"now":            u.Now(),
		"all":            all,
		"testUrl":        u.testUrl,
		"expectedStatus": u.expectedStatus,
		"fixed":          u.selected,
		"hidden":         u.Hidden,
		"icon":           u.Icon,
	})
}

func (u *URLTest) Providers() []P.ProxyProvider {
	return u.providers
}

func (u *URLTest) Proxies() []C.Proxy {
	return u.GetProxies(false)
}

func (u *URLTest) URLTest(ctx context.Context, url string, expectedStatus utils.IntRanges[uint16]) (map[string]uint16, error) {
	return u.GroupBase.URLTest(ctx, u.testUrl, expectedStatus)
}

func (u *URLTest) delayForComparison(proxy C.Proxy) float64 {
	if !proxy.AliveForTestUrl(u.testUrl) {
		return math.Inf(1)
	}

	delay := float64(proxy.LastDelayForTestUrl(u.testUrl))
	factor := 1.0
	matched := false
	for _, policy := range u.policyPriority {
		if match, _ := policy.regex.MatchString(proxy.Name()); match {
			if !matched || policy.factor < factor {
				factor = policy.factor
				matched = true
			}
		}
	}
	if !matched {
		return delay
	}
	return delay * factor
}

func parseURLTestOption(option *GroupCommonOption) ([]urlTestOption, error) {
	opts := []urlTestOption{}

	// tolerance
	if option.Tolerance != 0 {
		opts = append(opts, urlTestWithTolerance(uint16(option.Tolerance)))
	}

	if strings.TrimSpace(option.PolicyPriority) != "" {
		policyPriority, err := parsePolicyPriority(option.PolicyPriority)
		if err != nil {
			return nil, err
		}
		opts = append(opts, urlTestWithPolicyPriority(policyPriority))
	}

	return opts, nil
}

func parsePolicyPriority(raw string) ([]policyPriorityRule, error) {
	entries := strings.Split(raw, ";")
	policies := make([]policyPriorityRule, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		separator := strings.LastIndex(entry, ":")
		if separator <= 0 || separator == len(entry)-1 {
			return nil, fmt.Errorf("invalid policy-priority entry %q", entry)
		}

		pattern := strings.TrimSpace(entry[:separator])
		if pattern == "" {
			return nil, fmt.Errorf("invalid policy-priority entry %q", entry)
		}

		regex, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			return nil, fmt.Errorf("invalid policy-priority regex %q: %w", pattern, err)
		}

		factor, err := strconv.ParseFloat(strings.TrimSpace(entry[separator+1:]), 64)
		if err != nil {
			return nil, fmt.Errorf("invalid policy-priority factor in %q: %w", entry, err)
		}
		if math.IsNaN(factor) || math.IsInf(factor, 0) || factor <= 0 {
			return nil, fmt.Errorf("invalid policy-priority factor in %q: must be a finite number greater than 0", entry)
		}

		policies = append(policies, policyPriorityRule{
			pattern: pattern,
			regex:   regex,
			factor:  factor,
		})
	}
	return policies, nil
}

func NewURLTest(option *GroupCommonOption, providers []P.ProxyProvider, options ...urlTestOption) *URLTest {
	urlTest := &URLTest{
		GroupBase: NewGroupBase(GroupBaseOption{
			Name:           option.Name,
			Type:           C.URLTest,
			Filter:         option.Filter,
			ExcludeFilter:  option.ExcludeFilter,
			ExcludeType:    option.ExcludeType,
			TestTimeout:    option.TestTimeout,
			MaxFailedTimes: option.MaxFailedTimes,
			Providers:      providers,
		}),
		fastSingle:     singledo.NewSingle[C.Proxy](time.Second * 10),
		disableUDP:     option.DisableUDP,
		testUrl:        option.URL,
		expectedStatus: option.ExpectedStatus,
		Hidden:         option.Hidden,
		Icon:           option.Icon,
	}

	for _, option := range options {
		option(urlTest)
	}

	return urlTest
}
