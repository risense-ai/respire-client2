// Command bootstrap-metadata is the read-only metadata helper behind
// scripts/fetch-macos-components.sh. It selects the CLI and managed Runtime
// releases for one channel/target pair through the same normalization and
// validation rules the client's online installer uses (internal/host), and
// prints the fetch plan (asset identities, hashes, download URLs) as JSON.
// It never writes files and never downloads binaries.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"respire-client2/internal/host"
)

type fetchPlan struct {
	SchemaVersion int         `json:"schema_version"`
	Target        string      `json:"target"`
	Channel       string      `json:"channel"`
	CLI           cliPlan     `json:"cli"`
	Runtime       runtimePlan `json:"runtime"`
}

type cliPlan struct {
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	BinaryFile string `json:"binary_file"`
	BinaryURL  string `json:"binary_url"`
}

type runtimePlan struct {
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	SHA256      string `json:"sha256"`
	BinaryFile  string `json:"binary_file"`
	BinaryURL   string `json:"binary_url"`
	ArchiveFile string `json:"archive_file"`
	ArchiveHash string `json:"archive_sha256"`
	ArchiveURL  string `json:"archive_url"`
	MinCLI      string `json:"min_cli"`
	MaxCLI      string `json:"max_cli"`
}

// readInput accepts https URLs (real release sources) and plain paths
// (checked-in fixtures), so tests and CI run one code path.
func readInput(source string) ([]byte, error) {
	if !strings.HasPrefix(source, "https://") && !strings.HasPrefix(source, "http://") {
		return os.ReadFile(source)
	}
	req, err := http.NewRequest("GET", source, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Respire-Client2-Build")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", source, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 2<<20))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bootstrap-metadata: "+format+"\n", args...)
	os.Exit(1)
}

// assetURL resolves a release asset download location. Fixture mode keeps the
// by-name lookup but allows local file paths, since only the canonical-URL
// check (never the identity checks) is about GitHub's URL layout.
func assetURL(r host.ReleaseInfo, repo, name string, fixtures bool) (string, error) {
	url, err := host.ReleaseAssetURL(r, repo, name)
	if err == nil || !fixtures {
		return url, err
	}
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("%s release does not carry %s", r.Tag, name)
}

func main() {
	channel := flag.String("channel", "stable", "release channel: stable or dev")
	target := flag.String("target", "", "runtime target triple, e.g. aarch64-apple-darwin")
	runtimeReleases := flag.String("runtime-releases", "", "runtime release list URL or file")
	cliReleases := flag.String("cli-releases", "", "CLI release list URL or file")
	fixtures := flag.Bool("fixtures", false, "allow local-file asset URLs (offline fixture runs)")
	flag.Parse()
	if *channel != "stable" && *channel != "dev" {
		fail("unsupported channel %q", *channel)
	}
	if *target == "" || *runtimeReleases == "" || *cliReleases == "" {
		fail("--target, --runtime-releases and --cli-releases are required")
	}

	var runtimeList, cliList []host.ReleaseInfo
	for _, spec := range []struct {
		name string
		src  string
		dst  *[]host.ReleaseInfo
	}{{"runtime-releases", *runtimeReleases, &runtimeList}, {"cli-releases", *cliReleases, &cliList}} {
		b, err := readInput(spec.src)
		if err != nil {
			fail("read %s: %v", spec.name, err)
		}
		if err := json.Unmarshal(b, spec.dst); err != nil {
			fail("parse %s: %v", spec.name, err)
		}
	}

	runtimeLatest, err := host.SelectRelease(runtimeList, *channel)
	if err != nil {
		fail("no published Runtime release on the %s channel", *channel)
	}
	cliLatest, err := host.SelectRelease(cliList, *channel)
	if err != nil {
		fail("no published CLI release on the %s channel", *channel)
	}

	// The build metadata rides on the chosen release as an asset; resolve it
	// through the same canonical-URL rule the client enforces.
	runtimeBuildURL, err := assetURL(runtimeLatest, host.RuntimeRepo, "runtime-build-"+*target+".json", *fixtures)
	if err != nil {
		fail("Runtime %s: %v", runtimeLatest.Tag, err)
	}
	cliBuildURL, err := assetURL(cliLatest, host.CLIRepo, "cli-build-"+*target+".json", *fixtures)
	if err != nil {
		fail("CLI %s: %v", cliLatest.Tag, err)
	}
	runtimeBuild, err := readInput(runtimeBuildURL)
	if err != nil {
		fail("read runtime build metadata: %v", err)
	}
	cliBuild, err := readInput(cliBuildURL)
	if err != nil {
		fail("read CLI build metadata: %v", err)
	}

	meta, err := host.NormalizeRuntimeMetadata(runtimeBuild)
	if err != nil {
		fail("normalize Runtime metadata: %v", err)
	}
	if err := host.CheckRuntimeMetadata(meta, runtimeLatest.Tag, *target); err != nil {
		fail("Runtime metadata: %v", err)
	}
	cliMeta, err := host.NormalizeCLIMetadata(cliBuild, cliLatest.Tag, *target)
	if err != nil {
		fail("CLI metadata: %v", err)
	}
	if err := host.CheckCLICompat(cliMeta.Version, meta.Compat); err != nil {
		fail("compat: %v", err)
	}
	runtimeBinaryURL, err := assetURL(runtimeLatest, host.RuntimeRepo, meta.Binary, *fixtures)
	if err != nil {
		fail("Runtime %s: %v", runtimeLatest.Tag, err)
	}
	runtimeArchiveURL, err := assetURL(runtimeLatest, host.RuntimeRepo, meta.ArchiveFile, *fixtures)
	if err != nil {
		fail("Runtime %s: %v", runtimeLatest.Tag, err)
	}
	cliBinaryURL, err := assetURL(cliLatest, host.CLIRepo, cliMeta.Binary, *fixtures)
	if err != nil {
		fail("CLI %s: %v", cliLatest.Tag, err)
	}

	plan := fetchPlan{
		SchemaVersion: 1, Target: *target, Channel: *channel,
		CLI: cliPlan{
			Version: cliMeta.Version, SHA256: cliMeta.BinaryHash,
			BinaryFile: cliMeta.Binary, BinaryURL: cliBinaryURL,
		},
		Runtime: runtimePlan{
			Version: meta.RuntimeVersion, Tag: meta.Tag, SHA256: meta.BinaryHash,
			BinaryFile: meta.Binary, BinaryURL: runtimeBinaryURL,
			ArchiveFile: meta.ArchiveFile, ArchiveHash: meta.ArchiveHash, ArchiveURL: runtimeArchiveURL,
			MinCLI: meta.Compat.MinCLI, MaxCLI: meta.Compat.MaxCLI,
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		fail("encode fetch plan: %v", err)
	}
}
