package outboundgroup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/common/utils"
	"github.com/metacubex/mihomo/component/ca"
	"github.com/metacubex/mihomo/component/wildcard"
	C "github.com/metacubex/mihomo/constant"
	P "github.com/metacubex/mihomo/constant/provider"
	"github.com/metacubex/mihomo/log"

	"github.com/metacubex/http"
)

type SelectorOption struct {
	// DefaultSelected 为上游选项：仅按精确名称指定初始选中的节点。
	DefaultSelected string `group:"default-selected,omitempty"`
	// Default 为 fork 自定义项：支持精确名称、前缀、通配符；
	// 命中多个候选时后台探测下载，挑选第一个真正可用的节点。
	// 与 DefaultSelected 同时配置时 Default 优先。
	Default string `group:"default,omitempty"`
	// expectedStatus 不从配置解码（group:"-"），由 ParseProxyGroup 注入 group 的
	// expected-status，供 Default 前缀/通配符探测时校验响应状态码。
	expectedStatus utils.IntRanges[uint16] `group:"-"`
}

const (
	selectorDefaultProbeDuration    = 3 * time.Second
	selectorDefaultProbeIdleTimeout = time.Second
)

var selectorDefaultProbeDownload = probeSelectorDefaultDownload

type Selector struct {
	*GroupBase
	disableUDP bool

	selectedMux         sync.RWMutex
	selected            string
	defaultName         string
	defaultManaged      bool
	defaultProbeStarted bool

	testUrl        string
	expectedStatus utils.IntRanges[uint16]
}

// DialContext implements C.ProxyAdapter
func (s *Selector) DialContext(ctx context.Context, metadata *C.Metadata) (C.Conn, error) {
	c, err := s.selectedProxy(true).DialContext(ctx, metadata)
	if err == nil {
		c.AppendToChains(s)
	}
	return c, err
}

// ListenPacketContext implements C.ProxyAdapter
func (s *Selector) ListenPacketContext(ctx context.Context, metadata *C.Metadata) (C.PacketConn, error) {
	pc, err := s.selectedProxy(true).ListenPacketContext(ctx, metadata)
	if err == nil {
		pc.AppendToChains(s)
	}
	return pc, err
}

// SupportUDP implements C.ProxyAdapter
func (s *Selector) SupportUDP() bool {
	if s.disableUDP {
		return false
	}

	return s.selectedProxy(false).SupportUDP()
}

// IsL3Protocol implements C.ProxyAdapter
func (s *Selector) IsL3Protocol(metadata *C.Metadata) bool {
	return s.selectedProxy(false).IsL3Protocol(metadata)
}

// MarshalJSON implements C.ProxyAdapter
func (s *Selector) MarshalJSON() ([]byte, error) {
	all := []string{}
	for _, proxy := range s.GetProxies(false) {
		all = append(all, proxy.Name())
	}
	// When testurl is the default value
	// do not append a value to ensure that the web dashboard follows the settings of the dashboard
	var url string
	if s.testUrl != C.DefaultTestURL {
		url = s.testUrl
	}

	return json.Marshal(map[string]any{
		"type":          s.Type().String(),
		"now":           s.Now(),
		"all":           all,
		"testUrl":       url,
		"hidden":        s.Hidden(),
		"icon":          s.Icon(),
		"emptyFallback": s.EmptyFallback().Name(),
	})
}

func (s *Selector) Now() string {
	return s.selectedProxy(false).Name()
}

func (s *Selector) Set(name string) error {
	for _, proxy := range s.GetProxies(false) {
		if proxy.Name() == name {
			s.setSelected(name, false)
			return nil
		}
	}

	return errors.New("proxy not exist")
}

func (s *Selector) ForceSet(name string) {
	s.setSelected(name, false)
}

// Unwrap implements C.ProxyAdapter
func (s *Selector) Unwrap(metadata *C.Metadata, touch bool) C.Proxy {
	return s.selectedProxy(touch)
}

func (s *Selector) selectedProxy(touch bool) C.Proxy {
	proxies := s.GetProxies(touch)
	selected, defaultManaged := s.selection()
	var fallback C.Proxy
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		if fallback == nil {
			fallback = proxy
		}
		if proxy.Name() == selected {
			return proxy
		}
	}

	if defaultManaged {
		if proxy := s.resolveDefaultSelection(proxies); proxy != nil {
			return proxy
		}
	}

	if fallback != nil {
		return fallback
	}

	return proxies[0]
}

func (s *Selector) hasProxy(name string) bool {
	for _, proxy := range s.GetProxies(false) {
		if proxy == nil {
			continue
		}
		if proxy.Name() == name {
			return true
		}
	}

	return false
}

func (s *Selector) hasDefaultMatch(name string) bool {
	for _, proxy := range s.GetProxies(false) {
		if proxy == nil {
			continue
		}
		if defaultPatternMatch(name, proxy.Name()) {
			return true
		}
	}

	return false
}

func (s *Selector) resolveDefaultSelection(proxies []C.Proxy) C.Proxy {
	if s.defaultName == "" {
		return nil
	}

	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		if proxy.Name() == s.defaultName {
			s.setDefaultSelected(proxy.Name())
			return proxy
		}
	}

	candidates := s.defaultPatternCandidates(proxies)
	if len(candidates) == 0 {
		return nil
	}

	selected := candidates[0]
	s.setDefaultSelected(selected.Name())
	return selected
}

func (s *Selector) defaultPatternCandidates(proxies []C.Proxy) []C.Proxy {
	var candidates []C.Proxy
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		if defaultPatternMatch(s.defaultName, proxy.Name()) {
			candidates = append(candidates, proxy)
		}
	}
	return candidates
}

func (s *Selector) StartDefaultSelection() {
	if !s.isDefaultManaged() {
		return
	}

	proxies := s.GetProxies(false)
	if exact := s.defaultExactProxy(proxies); exact != nil {
		s.setDefaultSelected(exact.Name())
		return
	}

	candidates := s.defaultPatternCandidates(proxies)
	if len(candidates) == 0 || !s.markDefaultProbeStarted() {
		return
	}

	s.setDefaultSelected(candidates[0].Name())
	log.Infoln("The select group [%s] default %s [%s] starts availability probing in background", s.Name(), defaultPatternKind(s.defaultName), s.defaultName)
	go func() {
		selected := s.selectDefaultPatternCandidate(candidates)
		s.setDefaultSelected(selected.Name())
	}()
}

func (s *Selector) defaultExactProxy(proxies []C.Proxy) C.Proxy {
	for _, proxy := range proxies {
		if proxy == nil {
			continue
		}
		if proxy.Name() == s.defaultName {
			return proxy
		}
	}
	return nil
}

func (s *Selector) selectDefaultPatternCandidate(candidates []C.Proxy) C.Proxy {
	last := candidates[len(candidates)-1]
	probeURL := s.testUrl
	if probeURL == "" {
		probeURL = C.DefaultTestURL
	}
	for _, proxy := range candidates {
		timeout := time.Duration(s.testTimeout)*time.Millisecond + selectorDefaultProbeDuration
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		size, err := selectorDefaultProbeDownload(ctx, proxy, probeURL, s.expectedStatus, selectorDefaultProbeDuration)
		cancel()
		if err == nil {
			log.Infoln("The select group [%s] default %s [%s] selected proxy [%s] after downloading %d bytes from %s", s.Name(), defaultPatternKind(s.defaultName), s.defaultName, proxy.Name(), size, probeURL)
			return proxy
		}
		log.Warnln("The select group [%s] default %s [%s] probe failed for proxy [%s]: %v", s.Name(), defaultPatternKind(s.defaultName), s.defaultName, proxy.Name(), err)
	}

	log.Warnln("The select group [%s] default %s [%s] has no reachable proxy, fallback to last matched proxy [%s]", s.Name(), defaultPatternKind(s.defaultName), s.defaultName, last.Name())
	return last
}

func defaultPatternMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if name == pattern {
		return true
	}
	if defaultPatternHasWildcard(pattern) {
		return wildcard.Match(pattern, name)
	}
	return strings.HasPrefix(name, pattern)
}

func defaultPatternHasWildcard(pattern string) bool {
	return strings.ContainsAny(pattern, "*?")
}

func defaultPatternKind(pattern string) string {
	if defaultPatternHasWildcard(pattern) {
		return "wildcard"
	}
	return "prefix"
}

func (s *Selector) selection() (string, bool) {
	s.selectedMux.RLock()
	defer s.selectedMux.RUnlock()
	return s.selected, s.defaultManaged
}

func (s *Selector) isDefaultManaged() bool {
	s.selectedMux.RLock()
	defer s.selectedMux.RUnlock()
	return s.defaultManaged
}

func (s *Selector) setSelected(name string, defaultManaged bool) {
	s.selectedMux.Lock()
	defer s.selectedMux.Unlock()
	s.selected = name
	s.defaultManaged = defaultManaged
	if !defaultManaged {
		s.defaultProbeStarted = false
	}
}

func (s *Selector) setDefaultSelected(name string) bool {
	s.selectedMux.Lock()
	defer s.selectedMux.Unlock()
	if !s.defaultManaged {
		return false
	}
	s.selected = name
	return true
}

func (s *Selector) markDefaultProbeStarted() bool {
	s.selectedMux.Lock()
	defer s.selectedMux.Unlock()
	if !s.defaultManaged || s.defaultProbeStarted {
		return false
	}
	s.defaultProbeStarted = true
	return true
}

func (s *Selector) Providers() []P.ProxyProvider {
	return s.providers
}

func (s *Selector) Proxies() []C.Proxy {
	return s.GetProxies(false)
}

func NewSelector(option GroupCommonOption, selectorOption SelectorOption, emptyFallback C.Proxy, providers []P.ProxyProvider) (*Selector, error) {
	// fork: default 由 selector 自动管理选中项（支持前缀/通配符），优先级高于上游的 default-selected
	selected := selectorOption.Default
	defaultManaged := selected != ""
	if selected == "" {
		selected = selectorOption.DefaultSelected
	}

	selector := &Selector{
		GroupBase: NewGroupBase(GroupBaseOption{
			Name:           option.Name,
			Type:           C.Selector,
			Hidden:         option.Hidden,
			Icon:           option.Icon,
			Filter:         option.Filter,
			ExcludeFilter:  option.ExcludeFilter,
			ExcludeType:    option.ExcludeType,
			TestTimeout:    option.TestTimeout,
			MaxFailedTimes: option.MaxFailedTimes,
			EmptyFallback:  emptyFallback,
			Providers:      providers,
		}),
		selected:       selected,
		defaultName:    selectorOption.Default,
		defaultManaged: defaultManaged,
		disableUDP:     option.DisableUDP,
		testUrl:        option.URL,
		expectedStatus: selectorOption.expectedStatus,
	}

	return selector, nil
}

func probeSelectorDefaultDownload(ctx context.Context, proxy C.Proxy, rawURL string, expectedStatus utils.IntRanges[uint16], duration time.Duration) (uint64, error) {
	metadata, err := metadataFromURL(rawURL)
	if err != nil {
		return 0, err
	}

	conn, err := proxy.DialContext(ctx, &metadata)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = conn.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "mihomo-select-default-probe")

	tlsConfig, err := ca.GetTLSConfig(ca.Option{})
	if err != nil {
		return 0, err
	}

	used := false
	transport := &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, errors.New("selector default probe only supports one connection")
			}
			used = true
			return conn, nil
		},
		TLSClientConfig: tlsConfig,
	}
	defer transport.CloseIdleConnections()

	client := http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if !expectedStatus.Check(uint16(resp.StatusCode)) {
		return 0, fmt.Errorf("unexpected status code %d, expected %s", resp.StatusCode, expectedStatus.String())
	}

	buf := make([]byte, 32*1024)
	start := time.Now()
	deadline := start.Add(duration)
	var total uint64
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(selectorDefaultProbeIdleTimeout)); err != nil {
			log.Debugln("The select group default probe could not set read deadline for proxy [%s]: %v", proxy.Name(), err)
		}
		n, err := resp.Body.Read(buf)
		if n > 0 {
			total += uint64(n)
			continue
		}
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return total, fmt.Errorf("no download bytes within %s", selectorDefaultProbeIdleTimeout)
			}
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}

	if total == 0 {
		return 0, errors.New("download returned no bytes")
	}

	return total, nil
}

func metadataFromURL(rawURL string) (addr C.Metadata, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return
	}

	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			err = fmt.Errorf("%s scheme not support", rawURL)
			return
		}
	}

	err = addr.SetRemoteAddress(net.JoinHostPort(u.Hostname(), port))
	return
}
