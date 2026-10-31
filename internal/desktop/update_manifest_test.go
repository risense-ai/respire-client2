package desktop

import "testing"

func TestArtifactURLs(t *testing.T) {
	base := "https://github.com/risense-ai/respire-client2/releases/download/v0.2.9/respire-windows-amd64.exe"
	item := manifestArtifact{Filename: "respire-windows-amd64.exe", URL: base, Size: 1, SHA256: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}
	urls, err := artifactURLs(item, "0.2.9", "respire-windows-amd64.exe")
	if err != nil || len(urls) != 2 || urls[1] != base {
		t.Fatalf("artifactURLs() = %q, %v", urls, err)
	}
	item.URL = "https://github.com/risense-ai/respire-client2/releases/download/v0.2.9/other.exe"
	if _, err = artifactURLs(item, "0.2.9", "respire-windows-amd64.exe"); err == nil {
		t.Fatal("artifactURLs() accepted a foreign artifact URL")
	}
}
