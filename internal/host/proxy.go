package host

// Proxy resolution for the client's outbound HTTP stack.
//
// Every outbound request (update manifests, release downloads, GitHub API,
// model weights) goes through one of two HTTP clients built in New(). Their
// transports share the resolver below, so a single setting covers all of
// them:
//
//  1. HTTP_PROXY / HTTPS_PROXY / NO_PROXY (and the lowercase spellings, plus
//     ALL_PROXY as a fallback) win whenever any of them is present. This
//     keeps scripted and terminal launches behaving as before.
//  2. Otherwise the operating system proxy is read: scutil on macOS, the
//     Internet Settings registry key on Windows. Linux has no portable
//     system proxy API here, so it stays on step 1.
//  3. Loopback and localhost never use a proxy; Go's httpproxy matcher also
//     honours NO_PROXY / ProxyOverride entries.
//
// The preference proxyMode=direct turns steps 1 and 2 off entirely. The
// result is cached briefly so the settings page can report it without
// spawning a helper on every state poll.

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Proxy modes stored in preferences.
const (
	ProxyModeSystem = "system"
	ProxyModeDirect = "direct"
)

var proxyModes = map[string]bool{ProxyModeSystem: true, ProxyModeDirect: true}

// proxySource labels reported to the UI and diagnostics.
const (
	proxySourceEnvironment = "environment"
	proxySourceSystem      = "system"
	proxySourceDirect      = "direct"
	proxySourceOff         = "off"
)

// systemProxyTTL bounds how stale a cached system proxy lookup may be. It
// lets a user flip the system proxy without restarting the client while
// keeping /api/state cheap.
const systemProxyTTL = 60 * time.Second

// ProxyInfo describes the proxy the client would use right now. Credentials
// embedded in a proxy URL are stripped before this leaves the process.
type ProxyInfo struct {
	Mode    string `json:"mode"`
	Source  string `json:"source"`
	HTTP    string `json:"http,omitempty"`
	HTTPS   string `json:"https,omitempty"`
	NoProxy string `json:"noProxy,omitempty"`
	Note    string `json:"note,omitempty"`
}

// systemProxy is one proxy configuration snapshot. Source is
// "environment", "system" or "direct"; Note explains why a system setting
// could not be used (for example a PAC URL).
type systemProxy struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
	Source     string
	Note       string
}

func (c systemProxy) key() string {
	return c.HTTPProxy + "\x00" + c.HTTPSProxy + "\x00" + c.NoProxy
}

func (c systemProxy) proxyFor(target *url.URL) (*url.URL, error) {
	cfg := &httpproxy.Config{HTTPProxy: c.HTTPProxy, HTTPSProxy: c.HTTPSProxy, NoProxy: c.NoProxy}
	return cfg.ProxyFunc()(target)
}

type proxyResolver struct {
	mu     sync.Mutex
	read   func() systemProxy
	now    func() time.Time
	loaded bool
	readAt time.Time
	cache  systemProxy
	// matcher memoizes the NO_PROXY matcher for the current snapshot.
	matcher      func(*url.URL) (*url.URL, error)
	matcherFor   string
	matcherReady bool
}

func newProxyResolver() *proxyResolver {
	return &proxyResolver{read: readSystemProxy, now: time.Now}
}

// config returns the effective proxy for this moment: environment first,
// then a cached system lookup.
func (r *proxyResolver) config() systemProxy {
	if cfg, ok := proxyFromEnvironment(); ok {
		return cfg
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded && r.now().Sub(r.readAt) < systemProxyTTL {
		return r.cache
	}
	cfg := r.read()
	r.cache, r.loaded, r.readAt = cfg, true, r.now()
	return cfg
}

// proxyFunc builds the Transport.Proxy callback. mode is consulted per
// request so flipping the preference applies without rebuilding clients.
func (r *proxyResolver) proxyFunc(mode func() string) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		if mode() == ProxyModeDirect {
			return nil, nil
		}
		return r.matcherForURL(req.URL)
	}
}

func (r *proxyResolver) matcherForURL(target *url.URL) (*url.URL, error) {
	cfg := r.config()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.matcherReady || r.matcherFor != cfg.key() {
		r.matcher = cfg.proxyFor
		r.matcherFor = cfg.key()
		r.matcherReady = true
	}
	return r.matcher(target)
}

// proxyFromEnvironment reports whether the process environment defines a
// proxy. A NO_PROXY value alone does not count: it only refines a proxy
// chosen by HTTP_PROXY/HTTPS_PROXY/ALL_PROXY, and must not hide the system
// proxy setting.
func proxyFromEnvironment() (systemProxy, bool) {
	httpsProxy := firstEnv("HTTPS_PROXY", "https_proxy")
	httpProxy := firstEnv("HTTP_PROXY", "http_proxy")
	allProxy := firstEnv("ALL_PROXY", "all_proxy")
	noProxy := firstEnv("NO_PROXY", "no_proxy")
	if httpsProxy == "" && httpProxy == "" && allProxy == "" {
		return systemProxy{}, false
	}
	if httpsProxy == "" {
		httpsProxy = allProxy
	}
	if httpProxy == "" {
		httpProxy = allProxy
	}
	return systemProxy{HTTPProxy: httpProxy, HTTPSProxy: httpsProxy, NoProxy: noProxy, Source: proxySourceEnvironment}, true
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// ProxyInfo reports the proxy currently in effect.
func (m *Manager) ProxyInfo() ProxyInfo {
	mode := ProxyModeSystem
	if prefs := m.Preferences(); proxyModes[prefs.ProxyMode] {
		mode = prefs.ProxyMode
	}
	info := ProxyInfo{Mode: mode}
	if mode == ProxyModeDirect {
		info.Source = proxySourceOff
		return info
	}
	if m.proxy == nil {
		info.Source = proxySourceDirect
		return info
	}
	cfg := m.proxy.config()
	info.Source = cfg.Source
	info.HTTP = redactProxyURL(cfg.HTTPProxy)
	info.HTTPS = redactProxyURL(cfg.HTTPSProxy)
	info.NoProxy = cfg.NoProxy
	info.Note = cfg.Note
	return info
}

// ProxyEnv appends the system proxy to a child process environment. It only
// fills variables that are not already set, and never overrides an explicit
// environment proxy. Used for helper processes the client spawns (plugin
// git clones), not for the client's own HTTP clients.
func (m *Manager) ProxyEnv(env []string) []string {
	if m == nil || m.proxy == nil || m.Preferences().ProxyMode == ProxyModeDirect {
		return env
	}
	cfg := m.proxy.config()
	if cfg.Source != proxySourceSystem {
		return env
	}
	present := map[string]bool{}
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		present[strings.ToLower(key)] = true
	}
	add := func(key, value string) {
		if value == "" || present[strings.ToLower(key)] {
			return
		}
		env = append(env, key+"="+value)
	}
	add("HTTP_PROXY", cfg.HTTPProxy)
	add("HTTPS_PROXY", cfg.HTTPSProxy)
	add("NO_PROXY", cfg.NoProxy)
	return env
}

// redactProxyURL drops embedded credentials before a proxy URL is shown or
// exported.
func redactProxyURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	return parsed.String()
}

// addressWithPort joins a host and an optional port. Hosts that already
// carry a port or a scheme are left alone.
func addressWithPort(host, port string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		return host
	}
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return "[" + host + "]"
		}
		return host
	}
	return net.JoinHostPort(host, port)
}

func withScheme(address, scheme string) string {
	if address == "" || strings.Contains(address, "://") {
		return address
	}
	return scheme + "://" + address
}

// parseScutilProxy interprets `scutil --proxy` output. It is a pure function
// so the macOS path can be tested on any platform.
//
// Example:
//
//	<dictionary> {
//	  ExceptionsList : <array> {
//	    0 : 127.0.0.1
//	  }
//	  HTTPEnable : 1
//	  HTTPPort : 7897
//	  HTTPProxy : 127.0.0.1
//	}
func parseScutilProxy(output string) systemProxy {
	values := map[string]string{}
	var exceptions []string
	inExceptions := false
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "ExceptionsList"):
			inExceptions = true
			continue
		case line == "}" || line == "<dictionary> {":
			inExceptions = false
			continue
		}
		key, value, ok := strings.Cut(line, " : ")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if inExceptions {
			// Entries are indexed ("0 : 127.0.0.1"). The <local> token has no
			// NO_PROXY equivalent; loopback is already bypassed by httpproxy.
			if value != "" && !strings.EqualFold(value, "<local>") {
				exceptions = append(exceptions, value)
			}
			continue
		}
		values[key] = value
	}

	result := systemProxy{Source: proxySourceDirect, NoProxy: strings.Join(exceptions, ",")}
	httpProxy := ""
	if values["HTTPEnable"] == "1" {
		httpProxy = withScheme(addressWithPort(values["HTTPProxy"], values["HTTPPort"]), "http")
	}
	httpsProxy := ""
	if values["HTTPSEnable"] == "1" {
		httpsProxy = withScheme(addressWithPort(values["HTTPSProxy"], values["HTTPSPort"]), "http")
	}
	// macOS often enables only one of the two; the same listener usually
	// serves both schemes, so mirror the configured side.
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" && values["SOCKSEnable"] == "1" {
		if socks := withScheme(addressWithPort(values["SOCKSProxy"], values["SOCKSPort"]), "socks5"); socks != "" {
			httpProxy, httpsProxy = socks, socks
		}
	}
	if httpProxy == "" && httpsProxy == "" {
		if values["ProxyAutoConfigEnable"] == "1" || values["ProxyAutoDiscoveryEnable"] == "1" {
			result.Note = "系统代理使用 PAC 自动配置，客户端无法解析；已直连"
		}
		return result
	}
	result.HTTPProxy, result.HTTPSProxy, result.Source = httpProxy, httpsProxy, proxySourceSystem
	return result
}

// splitWindowsProxyServer parses the ProxyServer registry value, which is
// either a bare host:port or a per-scheme list, for example
// "http=proxy:8080;https=proxy:8443;socks=proxy:1080".
func splitWindowsProxyServer(server string) (httpProxy, httpsProxy, socksProxy string) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", "", ""
	}
	if !strings.Contains(server, "=") {
		return server, server, ""
	}
	for _, part := range strings.Split(server, ";") {
		kind, address, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		address = strings.TrimSpace(address)
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "http":
			httpProxy = address
		case "https":
			httpsProxy = address
		case "socks", "socks5":
			socksProxy = address
		}
	}
	return httpProxy, httpsProxy, socksProxy
}

// windowsSystemProxy is the pure part of the Windows lookup.
func windowsSystemProxy(enabled bool, server, override, autoConfigURL string) systemProxy {
	if strings.TrimSpace(autoConfigURL) != "" {
		return systemProxy{Source: proxySourceDirect, Note: "系统代理使用 PAC 自动配置，客户端无法解析；已直连"}
	}
	if !enabled {
		return systemProxy{Source: proxySourceDirect}
	}
	httpProxy, httpsProxy, socksProxy := splitWindowsProxyServer(server)
	if httpProxy == "" {
		httpProxy = httpsProxy
	}
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	if httpProxy == "" && httpsProxy == "" && socksProxy != "" {
		socks := withScheme(socksProxy, "socks5")
		httpProxy, httpsProxy = socks, socks
	}
	httpProxy = withScheme(httpProxy, "http")
	httpsProxy = withScheme(httpsProxy, "http")
	if httpProxy == "" && httpsProxy == "" {
		return systemProxy{Source: proxySourceDirect}
	}
	return systemProxy{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    windowsNoProxy(override),
		Source:     proxySourceSystem,
	}
}

// windowsNoProxy converts the semicolon-separated ProxyOverride value into
// the comma-separated NO_PROXY syntax. <local> means "hosts without a dot";
// httpproxy has no equivalent, and loopback is bypassed anyway.
func windowsNoProxy(override string) string {
	var items []string
	for _, item := range strings.Split(override, ";") {
		item = strings.TrimSpace(item)
		if item == "" || strings.EqualFold(item, "<local>") {
			continue
		}
		items = append(items, item)
	}
	return strings.Join(items, ",")
}
