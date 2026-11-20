// This package is imported only by the native self-update test's Go overlay.
package selfupdate

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"respire-client2/internal/host"
)

type transport struct {
	address      string
	version      string
	windowsReady []<-chan struct{}
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Wails emits ApplicationStarted before its pending windows finish opening.
	// Hold the Client2 manifest until both controllers have navigated, so the
	// updater's Quit cannot interrupt their initialization on a slow runner.
	if strings.HasSuffix(req.URL.Path, "/client-updates.json") {
		for _, ready := range t.windowsReady {
			select {
			case <-ready:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
	}
	local := req.Clone(req.Context())
	local.Header.Set("X-Test-Client-Version", t.version)
	local.URL.Scheme, local.URL.Host = "http", t.address
	return http.DefaultTransport.RoundTrip(local)
}

func Attach(manager *host.Manager, version string, views ...*application.WebviewWindow) {
	address := os.Getenv("CLIENT2_NATIVE_UPDATE_FIXTURE")
	hostIP, _, err := net.SplitHostPort(address)
	if err != nil || hostIP != "127.0.0.1" {
		panic(fmt.Sprintf("invalid self-update fixture address %q", address))
	}
	fixture := transport{address: address, version: version}
	for _, view := range views {
		ready := make(chan struct{})
		complete := sync.OnceFunc(func() { close(ready) })
		view.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) { complete() })
		fixture.windowsReady = append(fixture.windowsReady, ready)
	}
	manager.HTTPClient.Transport = fixture
}
