package host

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type modelReleaseTransport func(*http.Request) (*http.Response, error)

func (f modelReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func fakeModelReleaseResponse(req *http.Request, code int, status, body string, headers http.Header) *http.Response {
	return &http.Response{
		StatusCode: code, Status: status, Header: headers, Request: req,
		ContentLength: int64(len(body)), Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestCPUORTKeepsApprovedOrderAndPinnedIntegrityFallback(t *testing.T) {
	approved := []string{
		"https://gh-proxy.com/" + m3CPUContainerURL,
		"https://gh-proxy.org/" + m3CPUContainerURL,
		m3CPUContainerURL,
	}
	m := &Manager{}
	m.prefs.ModelMirror = "https://hf-custom.example.invalid/base"
	var calls []string
	m.modelHTTPClient = &http.Client{Transport: modelReleaseTransport(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.URL.String())
		if req.Method != http.MethodGet || req.Header.Get("Range") != "bytes=0-3" ||
			req.Header.Get("Authorization") != "" || req.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("unexpected approved release request: %s %s", req.Method, req.URL)
		}
		switch req.URL.String() {
		case approved[0]:
			// Correct length is not a substitute for a pinned digest.
			return fakeModelReleaseResponse(req, 200, "200 OK", "bad!", nil), nil
		case approved[1]:
			return fakeModelReleaseResponse(req, 403, "403 Forbidden", "", nil), nil
		case approved[2]:
			return fakeModelReleaseResponse(req, 200, "200 OK", "good", nil), nil
		default:
			return nil, errors.New("unexpected host")
		}
	})}
	dest := filepath.Join(t.TempDir(), "model.ort")
	spec := modelTransferSpec([]byte("good"))
	if err := m.downloadModelFromSources(t.Context(), m3CPUContainerSources(), dest, spec); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, approved) {
		t.Fatalf("unexpected source order: got %q want %q", calls, approved)
	}
	if data, err := os.ReadFile(dest); err != nil || string(data) != "good" || !modelFileValid(dest, spec) {
		t.Fatalf("pinned output not published: %q, %v", data, err)
	}
}

func TestCPUORTFailedSourcesReportInitialURLsWithoutRedirectSecrets(t *testing.T) {
	const malformed = "https://test-user:SECRET_PROXY@example.invalid/%zz?sig=SECRET_MALFORMED"
	const redirected = "https://release-assets.githubusercontent.com/o/asset?sig=SECRET_SIGNED"
	m := &Manager{modelHTTPClient: &http.Client{Transport: modelReleaseTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case "https://gh-proxy.com/" + m3CPUContainerURL:
			return fakeModelReleaseResponse(req, 403, "403 SECRET_STATUS", "", nil), nil
		case "https://gh-proxy.org/" + m3CPUContainerURL:
			return fakeModelReleaseResponse(req, 302, "302 Found", "", http.Header{"Location": {malformed}}), nil
		case m3CPUContainerURL:
			return fakeModelReleaseResponse(req, 302, "302 Found", "", http.Header{"Location": {redirected}}), nil
		case redirected:
			return fakeModelReleaseResponse(req, 404, "404 SECRET_REDIRECT_STATUS", "", nil), nil
		default:
			return nil, errors.New("unexpected host")
		}
	})}}
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	dest := filepath.Join(t.TempDir(), "model.ort")
	err := m.downloadModelFromSources(ctx, m3CPUContainerSources(), dest, modelTransferSpec([]byte("good")))
	if err == nil {
		t.Fatal("failure was not returned")
	}
	want := []string{
		"https://gh-proxy.com/" + m3CPUContainerURL + ": HTTP 403",
		"https://gh-proxy.org/" + m3CPUContainerURL + ": 无法完成下载请求",
		m3CPUContainerURL + ": HTTP 404",
	}
	if lines := strings.Split(err.Error(), "\n"); !reflect.DeepEqual(lines, want) {
		t.Fatalf("unexpected source diagnostics: %q", lines)
	}
	var status *modelHTTPError
	var transport *url.Error
	if !errors.As(err, &status) || status.status != 403 || !errors.As(err, &transport) {
		t.Fatalf("wrapping lost an underlying typed cause: %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed transfer published an artifact")
	}
}

func TestCPUORTSourceDiagnosticKeepsFailureClassButNotTransportDetails(t *testing.T) {
	const privateURL = "https://user:SECRET_AUTH@cdn.example.invalid/?sig=SECRET_SIGNED"
	tests := []struct {
		cause error
		want  string
	}{
		{&url.Error{Op: "Get", URL: privateURL, Err: &net.DNSError{Name: "SECRET_DNS", Err: "SECRET_MESSAGE"}}, "无法解析下载域名"},
		{&modelHTTPError{status: 407, text: "407 SECRET_AUTH", proxy: true}, "代理 HTTP 407"},
		{&modelIntegrityError{file: "SECRET_FILE"}, "大小或 SHA-256 校验不匹配"},
		{errors.Join(errModelHeaderStall, context.Canceled), errModelHeaderStall.Error()},
	}
	for _, test := range tests {
		err := &modelReleaseSourceError{url: m3CPUContainerURL, cause: test.cause}
		if err.Error() != m3CPUContainerURL+": "+test.want || !errors.Is(err, test.cause) {
			t.Errorf("wrong safe error or cause: %v", err)
		}
	}
}
