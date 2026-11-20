// This package is imported only by the native self-update test's Go overlay.
package selfupdate

import (
	"fmt"
	"net"
	"net/http"
	"os"

	"respire-client2/internal/host"
)

type transport struct {
	address string
	version string
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	local := req.Clone(req.Context())
	local.Header.Set("X-Test-Client-Version", t.version)
	local.URL.Scheme, local.URL.Host = "http", t.address
	return http.DefaultTransport.RoundTrip(local)
}

func Attach(manager *host.Manager, version string) {
	address := os.Getenv("CLIENT2_NATIVE_UPDATE_FIXTURE")
	hostIP, _, err := net.SplitHostPort(address)
	if err != nil || hostIP != "127.0.0.1" {
		panic(fmt.Sprintf("invalid self-update fixture address %q", address))
	}
	manager.HTTPClient.Transport = transport{address, version}
}
