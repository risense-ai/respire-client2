package host

import (
	"net/http"
	"testing"
)

// Both outbound clients must be wired to the same resolver: a regression
// that gives one of them a direct-only transport would silently bypass the
// proxy for updates or model weights.
func TestOutboundClientsShareProxyResolver(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(name, "")
	}
	t.Setenv("RSRS_DATA_DIR", t.TempDir())
	t.Setenv("RSRS_RPC_PORT", "15199")
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/x/y/releases/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	clients := map[string]*http.Client{"update": m.HTTPClient, "model": m.modelClient()}
	for name, client := range clients {
		transport, ok := client.Transport.(*http.Transport)
		if !ok || transport.Proxy == nil {
			t.Fatalf("%s client has no proxy callback (%T)", name, client.Transport)
		}
	}

	modelTransport := clients["model"].Transport.(*http.Transport)
	modelTemplate := m.modelHTTPClient.Transport.(*http.Transport)
	updateTransport := m.HTTPClient.Transport.(*http.Transport)
	if modelTransport == modelTemplate || modelTransport == updateTransport ||
		modelTransport.ResponseHeaderTimeout != modelDownloadStall || modelTransport.MaxConnsPerHost != modelTemplate.MaxConnsPerHost ||
		modelTransport.OnProxyConnectResponse == nil || clients["model"].Timeout != 0 {
		t.Fatal("constructed manager lost the model-only setup policy or group pool")
	}
	second := m.modelClient().Transport.(*http.Transport)
	if second == modelTransport || second.MaxConnsPerHost != modelTemplate.MaxConnsPerHost ||
		modelTemplate.ResponseHeaderTimeout != 0 || modelTemplate.MaxConnsPerHost != 0 ||
		updateTransport.ResponseHeaderTimeout != 0 || updateTransport.OnProxyConnectResponse != nil {
		t.Fatal("model group policy mutated its template or update transport")
	}

	custom := modelTemplate.Clone()
	custom.MaxConnsPerHost = 3
	configured := &Manager{modelHTTPClient: &http.Client{Transport: custom}}
	candidate := configured.modelClient().Transport.(*http.Transport)
	if candidate == custom || candidate.MaxConnsPerHost != 3 {
		t.Fatal("intentional per-host limits must be preserved by the clone")
	}

	// direct mode is deterministic regardless of the host configuration.
	m.prefs.ProxyMode = ProxyModeDirect
	for name, client := range clients {
		proxy, err := client.Transport.(*http.Transport).Proxy(req)
		if err != nil || proxy != nil {
			t.Fatalf("%s client must bypass the proxy in direct mode, got %v (%v)", name, proxy, err)
		}
	}

	// A loopback request always bypasses the proxy, even in system mode.
	m.prefs.ProxyMode = ProxyModeSystem
	loopback, err := http.NewRequest("GET", m.Endpoint+"/api/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, client := range clients {
		proxy, err := client.Transport.(*http.Transport).Proxy(loopback)
		if err != nil || proxy != nil {
			t.Fatalf("%s client must keep loopback direct, got %v (%v)", name, proxy, err)
		}
	}
}
