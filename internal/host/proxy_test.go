package host

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

const scutilSample = `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.168.0.0/16
    2 : 10.0.0.0/8
    3 : 172.16.0.0/12
    4 : localhost
    5 : *.local
    6 : *.crashlytics.com
    7 : <local>
  }
  FTPPassive : 1
  HTTPEnable : 1
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  ProxyAutoConfigEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 7897
  SOCKSProxy : 127.0.0.1
}`

func TestParseScutilProxyReadsHTTPAndHTTPS(t *testing.T) {
	cfg := parseScutilProxy(scutilSample)
	if cfg.Source != proxySourceSystem {
		t.Fatalf("source = %q, want system", cfg.Source)
	}
	if cfg.HTTPProxy != "http://127.0.0.1:7897" || cfg.HTTPSProxy != "http://127.0.0.1:7897" {
		t.Fatalf("unexpected proxies: %+v", cfg)
	}
	wantNoProxy := "127.0.0.1,192.168.0.0/16,10.0.0.0/8,172.16.0.0/12,localhost,*.local,*.crashlytics.com"
	if cfg.NoProxy != wantNoProxy {
		t.Fatalf("noProxy = %q, want %q", cfg.NoProxy, wantNoProxy)
	}
}

func TestParseScutilProxyMirrorsSingleScheme(t *testing.T) {
	cfg := parseScutilProxy(`<dictionary> {
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : proxy.example
}`)
	if cfg.HTTPProxy != "http://proxy.example:8080" || cfg.HTTPSProxy != cfg.HTTPProxy {
		t.Fatalf("an HTTP-only setting must serve both schemes, got %+v", cfg)
	}
}

func TestParseScutilProxyFallsBackToSOCKS(t *testing.T) {
	cfg := parseScutilProxy(`<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 0
  SOCKSEnable : 1
  SOCKSPort : 1080
  SOCKSProxy : 127.0.0.1
}`)
	if cfg.HTTPProxy != "socks5://127.0.0.1:1080" || cfg.HTTPSProxy != cfg.HTTPProxy {
		t.Fatalf("SOCKS-only setting must be used, got %+v", cfg)
	}
}

func TestParseScutilProxyDisabledAndPAC(t *testing.T) {
	disabled := parseScutilProxy("<dictionary> {\n  HTTPEnable : 0\n}")
	if disabled.Source != proxySourceDirect || disabled.HTTPProxy != "" {
		t.Fatalf("disabled proxies must resolve to direct, got %+v", disabled)
	}
	pac := parseScutilProxy("<dictionary> {\n  ProxyAutoConfigEnable : 1\n  ProxyAutoConfigURLString : http://wpad/proxy.pac\n}")
	if pac.Source != proxySourceDirect || pac.Note == "" {
		t.Fatalf("PAC must stay direct with an explanation, got %+v", pac)
	}
}

func TestWindowsSystemProxyParsing(t *testing.T) {
	cfg := windowsSystemProxy(false, "proxy.example:8080", "", "")
	if cfg.Source != proxySourceDirect {
		t.Fatalf("disabled proxy must be direct, got %+v", cfg)
	}
	cfg = windowsSystemProxy(true, "proxy.example:8080", "localhost;*.local;<local>", "")
	if cfg.HTTPProxy != "http://proxy.example:8080" || cfg.HTTPSProxy != cfg.HTTPProxy {
		t.Fatalf("bare server must serve both schemes, got %+v", cfg)
	}
	if cfg.NoProxy != "localhost,*.local" {
		t.Fatalf("ProxyOverride mapping failed: %q", cfg.NoProxy)
	}
	cfg = windowsSystemProxy(true, "http=a:1;https=b:2;socks=c:3", "", "")
	if cfg.HTTPProxy != "http://a:1" || cfg.HTTPSProxy != "http://b:2" {
		t.Fatalf("per-scheme list failed: %+v", cfg)
	}
	cfg = windowsSystemProxy(true, "socks=c:3", "", "")
	if cfg.HTTPProxy != "socks5://c:3" || cfg.HTTPSProxy != cfg.HTTPProxy {
		t.Fatalf("SOCKS-only setting failed: %+v", cfg)
	}
	pac := windowsSystemProxy(true, "proxy.example:8080", "", "http://wpad/proxy.pac")
	if pac.Source != proxySourceDirect || pac.Note == "" {
		t.Fatalf("PAC must stay direct with an explanation, got %+v", pac)
	}
}

func TestEnvironmentProxyWinsOverSystem(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://env.example:3128")
	t.Setenv("HTTP_PROXY", "http://env.example:3128")
	t.Setenv("NO_PROXY", "example.com")
	t.Setenv("ALL_PROXY", "")
	resolver := &proxyResolver{
		read: func() systemProxy {
			t.Fatal("system lookup must not run when the environment defines a proxy")
			return systemProxy{}
		},
		now: time.Now,
	}
	cfg := resolver.config()
	if cfg.Source != proxySourceEnvironment || cfg.HTTPSProxy != "http://env.example:3128" {
		t.Fatalf("environment proxy must win, got %+v", cfg)
	}
	if cfg.NoProxy != "example.com" {
		t.Fatalf("NO_PROXY must be carried, got %q", cfg.NoProxy)
	}
}

func TestAllProxyFillsMissingSchemes(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("ALL_PROXY", "socks5://127.0.0.1:1080")
	cfg, ok := proxyFromEnvironment()
	if !ok {
		t.Fatal("ALL_PROXY alone must count as an environment proxy")
	}
	if cfg.HTTPProxy != "socks5://127.0.0.1:1080" || cfg.HTTPSProxy != cfg.HTTPProxy {
		t.Fatalf("ALL_PROXY must fill both schemes, got %+v", cfg)
	}
}

func TestSystemProxyCachedAndTTL(t *testing.T) {
	reads := 0
	now := time.Unix(0, 0)
	resolver := &proxyResolver{
		read: func() systemProxy {
			reads++
			return systemProxy{HTTPProxy: "http://cached:8080", HTTPSProxy: "http://cached:8080", Source: proxySourceSystem}
		},
		now: func() time.Time { return now },
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	if target, err := resolver.proxyForRemote(t, "https://example.com/x"); err != nil || target != "http://cached:8080" {
		t.Fatalf("first lookup = %v, %v", target, err)
	}
	now = now.Add(systemProxyTTL / 2)
	if target, _ := resolver.proxyForRemote(t, "https://example.com/x"); target != "http://cached:8080" {
		t.Fatalf("cached lookup = %q", target)
	}
	if reads != 1 {
		t.Fatalf("system settings read %d times inside the TTL, want 1", reads)
	}
	now = now.Add(systemProxyTTL)
	if _, _ = resolver.proxyForRemote(t, "https://example.com/x"); reads != 2 {
		t.Fatalf("expired cache must refresh, got %d reads", reads)
	}
}

func TestLoopbackAndNoProxyBypass(t *testing.T) {
	cfg := systemProxy{HTTPProxy: "http://proxy:8080", HTTPSProxy: "http://proxy:8080", NoProxy: "internal.example", Source: proxySourceSystem}
	for _, target := range []string{"http://127.0.0.1:15169/api/state", "http://localhost:4173/", "http://[::1]:15169/"} {
		parsed, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		got, err := cfg.proxyFor(parsed)
		if err != nil || got != nil {
			t.Fatalf("%s must bypass the proxy, got %v (%v)", target, got, err)
		}
	}
	parsed, _ := url.Parse("https://internal.example/x")
	if got, _ := cfg.proxyFor(parsed); got != nil {
		t.Fatalf("NO_PROXY entry must bypass the proxy, got %v", got)
	}
	parsed, _ = url.Parse("https://api.github.com/x")
	if got, _ := cfg.proxyFor(parsed); got == nil || got.String() != "http://proxy:8080" {
		t.Fatalf("external host must use the proxy, got %v", got)
	}
}

func TestNoProxyAloneDoesNotMaskSystemProxy(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("NO_PROXY", "example.com")
	if _, ok := proxyFromEnvironment(); ok {
		t.Fatal("NO_PROXY alone must not count as an environment proxy")
	}
}

func TestProxyFuncHonoursDirectMode(t *testing.T) {
	resolver := &proxyResolver{
		read: func() systemProxy {
			return systemProxy{HTTPProxy: "http://proxy:8080", HTTPSProxy: "http://proxy:8080", Source: proxySourceSystem}
		},
		now: time.Now,
	}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	req, _ := http.NewRequest("GET", "https://api.github.com/x", nil)
	direct := resolver.proxyFunc(func() string { return ProxyModeDirect })
	if got, err := direct(req); err != nil || got != nil {
		t.Fatalf("direct mode must not proxy, got %v (%v)", got, err)
	}
	system := resolver.proxyFunc(func() string { return ProxyModeSystem })
	if got, err := system(req); err != nil || got == nil {
		t.Fatalf("system mode must proxy, got %v (%v)", got, err)
	}
}

func TestProxyInfoRedactsCredentials(t *testing.T) {
	m := &Manager{prefs: defaultPreferences(), proxy: &proxyResolver{
		read: func() systemProxy {
			return systemProxy{HTTPProxy: "http://user:secret@proxy:8080", HTTPSProxy: "http://user:secret@proxy:8080", Source: proxySourceSystem}
		},
		now: time.Now,
	}}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	info := m.ProxyInfo()
	if info.Source != proxySourceSystem || info.HTTPS != "http://proxy:8080" {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.HTTP != info.HTTPS {
		t.Fatalf("both schemes must be reported: %+v", info)
	}
	m.prefs.ProxyMode = ProxyModeDirect
	info = m.ProxyInfo()
	if info.Source != proxySourceOff || info.HTTPS != "" {
		t.Fatalf("direct mode must report no proxy, got %+v", info)
	}
}

func TestProxyEnvOnlyFillsMissingVariables(t *testing.T) {
	m := &Manager{prefs: defaultPreferences(), proxy: &proxyResolver{
		read: func() systemProxy {
			return systemProxy{HTTPProxy: "http://proxy:8080", HTTPSProxy: "http://proxy:8080", Source: proxySourceSystem}
		},
		now: time.Now,
	}}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	env := m.ProxyEnv([]string{"PATH=/usr/bin", "https_proxy=http://explicit:1"})
	joined := map[string]string{}
	for _, item := range env {
		key, value, _ := cutEnv(item)
		joined[key] = value
	}
	if joined["https_proxy"] != "http://explicit:1" {
		t.Fatalf("explicit environment proxy must survive, got %q", joined["https_proxy"])
	}
	if joined["HTTPS_PROXY"] != "http://proxy:8080" && joined["https_proxy"] != "http://explicit:1" {
		t.Fatalf("system proxy must be injected when unset, got %v", joined)
	}
	m.prefs.ProxyMode = ProxyModeDirect
	if got := m.ProxyEnv([]string{"PATH=/usr/bin"}); len(got) != 1 {
		t.Fatalf("direct mode must not inject proxy variables, got %v", got)
	}
}

func cutEnv(item string) (string, string, bool) {
	for i := 0; i < len(item); i++ {
		if item[i] == '=' {
			return item[:i], item[i+1:], true
		}
	}
	return item, "", false
}

// proxyForRemote is a test helper on *proxyResolver: it runs the same
// matching path a real request takes, without needing a live *http.Request.
func (r *proxyResolver) proxyForRemote(t *testing.T, target string) (string, error) {
	t.Helper()
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse %q: %v", target, err)
	}
	proxy, err := r.matcherForURL(parsed)
	if proxy == nil {
		return "", err
	}
	return proxy.String(), err
}
