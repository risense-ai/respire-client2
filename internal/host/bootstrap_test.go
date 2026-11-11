package host

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildRuntimeTree materializes a minimal but genuinely verifiable managed
// Runtime tree (binary + notices + core-runtime.json listing both) under dir.
func buildRuntimeTree(t *testing.T, dir, target string) (binaryHash string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "core-notices"), 0700); err != nil {
		t.Fatal(err)
	}
	// The runtime binary must be a valid image for the architecture check,
	// so use a copy of the test binary itself under the triple-qualified
	// archive name the release pipeline produces.
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(testBin)
	if err != nil {
		t.Fatal(err)
	}
	target, targetErr := runtimeTarget()
	if targetErr != nil {
		t.Fatal(targetErr)
	}
	binary := filepath.Join(dir, "rsrs-runtime-"+target)
	if err := os.WriteFile(binary, b, 0700); err != nil {
		t.Fatal(err)
	}
	notices := filepath.Join(dir, "core-notices", "NOTICES.txt")
	if err := os.WriteFile(notices, []byte("runtime notices\n"), 0600); err != nil {
		t.Fatal(err)
	}
	noticesHash, err := fileHash(notices)
	if err != nil {
		t.Fatal(err)
	}
	binaryHash, err = fileHash(binary)
	if err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"target":         target,
		"files": []map[string]string{
			{"path": "rsrs-runtime-" + target, "sha256": binaryHash},
			{"path": "core-notices/NOTICES.txt", "sha256": noticesHash},
		},
	}
	b, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "core-runtime.json"), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return binaryHash
}

// buildPayload assembles a complete bootstrap payload fixture and points
// discovery at it. The CLI slot uses the test binary itself so the
// architecture check genuinely passes on the host running the test.
func buildPayload(t *testing.T, cliVersion string) string {
	t.Helper()
	target, err := runtimeTarget()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, cliBinary())
	b, err := os.ReadFile(testBin)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cli, b, 0700); err != nil {
		t.Fatal(err)
	}
	cliHash, err := fileHash(cli)
	if err != nil {
		t.Fatal(err)
	}
	runtimeHash := buildRuntimeTree(t, filepath.Join(dir, bootstrapRuntimeDir), target)
	// Mirror the build script: the downloaded binary is placed next to the
	// archive tree as rsrs-runtime, which verifyRuntime checks first.
	alias := filepath.Join(dir, bootstrapRuntimeDir, runtimeBinary())
	if err := os.WriteFile(alias, b, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema_version": 1,
		"target":         target,
		"channel":        "dev",
		"cli":            map[string]string{"version": cliVersion, "sha256": cliHash},
		"runtime": map[string]string{
			"version": "1.0.21", "sha256": runtimeHash,
			"min_cli": "0.4.0", "max_cli": "*",
		},
	}
	b, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, bootstrapManifestFile), append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(bootstrapEnvOverride, dir)
	return dir
}

func TestBootstrapPayloadDiscovery(t *testing.T) {
	t.Run("missing payload keeps the online path", func(t *testing.T) {
		t.Setenv(bootstrapEnvOverride, filepath.Join(t.TempDir(), "absent"))
		if _, err := readBootstrapPayload(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent payload must report ErrNotExist, got %v", err)
		}
	})
	t.Run("valid payload verifies in full", func(t *testing.T) {
		dir := buildPayload(t, "0.4.1")
		payload, err := readBootstrapPayload()
		if err != nil {
			t.Fatalf("valid payload must verify: %v", err)
		}
		if payload.Dir != dir || payload.Manifest.CLI.Version != "0.4.1" || payload.Manifest.Runtime.Version != "1.0.21" {
			t.Fatalf("payload identity drifted: %+v", payload.Manifest)
		}
	})
	t.Run("foreign-platform payload is invisible", func(t *testing.T) {
		dir := buildPayload(t, "0.4.1")
		b, err := os.ReadFile(filepath.Join(dir, bootstrapManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(b, &manifest); err != nil {
			t.Fatal(err)
		}
		if manifest["target"] == "x86_64-unknown-linux-gnu" {
			manifest["target"] = "aarch64-apple-darwin"
		} else {
			manifest["target"] = "x86_64-unknown-linux-gnu"
		}
		b, _ = json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, bootstrapManifestFile), b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBootstrapPayload(); err == nil || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("foreign platform must reject with a specific error, got %v", err)
		}
	})
	t.Run("tampered payload is rejected", func(t *testing.T) {
		dir := buildPayload(t, "0.4.1")
		// CLI tamper: rewrite bytes, keep the manifest hash.
		if err := os.WriteFile(filepath.Join(dir, cliBinary()), []byte("evil"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := readBootstrapPayload(); err == nil {
			t.Fatal("tampered CLI must be rejected")
		}
		// Runtime tamper: flip bytes inside a manifest-listed file.
		dir = buildPayload(t, "0.4.1")
		notices := filepath.Join(dir, bootstrapRuntimeDir, "core-notices", "NOTICES.txt")
		if err := os.WriteFile(notices, []byte("tampered notices\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBootstrapPayload(); err == nil {
			t.Fatal("tampered Runtime tree must be rejected")
		}
	})
	t.Run("unknown manifest fields reject", func(t *testing.T) {
		dir := buildPayload(t, "0.4.1")
		b, err := os.ReadFile(filepath.Join(dir, bootstrapManifestFile))
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(b, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["surprise"] = true
		b, _ = json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(dir, bootstrapManifestFile), b, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBootstrapPayload(); err == nil {
			t.Fatal("unknown manifest fields must reject the payload")
		}
	})
}

// cliProbeFixture controls the version probe during CLI install tests: the
// staging copy (matched by its .rsrs-payload- temp prefix) and the installed
// CLI each report a version or fail as "unparseable".
type cliProbeFixture struct {
	tmpVersion string
	tmpBroken  bool
	destAbsent bool // no probe answer for the installed CLI
	destBroken bool
	destVer    string
}

func stubCLIProbe(t *testing.T, f cliProbeFixture) {
	t.Helper()
	original := probeCLIVersion
	probeCLIVersion = func(_ context.Context, path string) (string, error) {
		if strings.Contains(filepath.Base(path), ".rsrs-payload-") {
			if f.tmpBroken {
				return "", errors.New("探测失败")
			}
			return f.tmpVersion, nil
		}
		if f.destAbsent {
			return "", fmt.Errorf("no probe fixture for %s", path)
		}
		if f.destBroken {
			return "", errors.New("unparseable output")
		}
		return f.destVer, nil
	}
	t.Cleanup(func() { probeCLIVersion = original })
}

func newInstallTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	m := &Manager{Root: filepath.Join(root, "client2"), DataRoot: root,
		Endpoint: "http://127.0.0.1:1", HTTPClient: &http.Client{}, prefs: defaultPreferences(),
		localClient: &http.Client{Transport: &http.Transport{Proxy: nil}}}
	if err := os.MkdirAll(m.Root, 0700); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInstallBootstrapCLIAtomicPlacement(t *testing.T) {
	t.Run("fresh install publishes the payload CLI", func(t *testing.T) {
		buildPayload(t, "1.2.3")
		payload, err := readBootstrapPayload()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destAbsent: true})
		if err = m.installBootstrapCLI(context.Background(), &payload); err != nil {
			t.Fatalf("fresh install failed: %v", err)
		}
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("CLI not installed: %v", err)
		}
		if info.Mode().Perm() != 0700 {
			t.Fatalf("installed CLI must be 0700, got %v", info.Mode().Perm())
		}
		if hash, err := fileHash(dest); err != nil || hash != payload.Manifest.CLI.SHA256 {
			t.Fatalf("installed CLI bytes wrong: %v %s", err, hash)
		}
		entries, _ := os.ReadDir(filepath.Dir(dest))
		if len(entries) != 1 {
			t.Fatalf("staging leftovers in bin dir: %v", entries)
		}
	})
	t.Run("equal version refreshes from the verified payload", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, []byte("same version but corrupt bytes"), 0700); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destVer: "1.2.3"})
		if err = m.installBootstrapCLI(context.Background(), &payload); err != nil {
			t.Fatalf("equal-version reinstall failed: %v", err)
		}
		if hash, _ := fileHash(dest); hash != payload.Manifest.CLI.SHA256 {
			t.Fatal("equal version must still refresh to the verified payload bytes")
		}
	})
	t.Run("older version upgrades", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, []byte("old"), 0700); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destVer: "1.0.0"})
		if err = m.installBootstrapCLI(context.Background(), &payload); err != nil {
			t.Fatalf("upgrade failed: %v", err)
		}
		if hash, _ := fileHash(dest); hash != payload.Manifest.CLI.SHA256 {
			t.Fatal("older CLI must be upgraded to the payload version")
		}
	})
	t.Run("newer version is kept untouched", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, []byte("newer-installed-cli"), 0700); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destVer: "9.9.9"})
		if err = m.installBootstrapCLI(context.Background(), &payload); err != nil {
			t.Fatalf("newer CLI must be kept without error: %v", err)
		}
		if b, _ := os.ReadFile(dest); string(b) != "newer-installed-cli" {
			t.Fatal("a newer installed CLI must never be overwritten")
		}
	})
	t.Run("unparseable existing file is not clobbered", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, []byte("mystery-file"), 0700); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destBroken: true})
		err = m.installBootstrapCLI(context.Background(), &payload)
		if err == nil || !strings.Contains(err.Error(), "未覆盖") {
			t.Fatalf("unparseable existing file must abort with a fix path, got %v", err)
		}
		if b, _ := os.ReadFile(dest); string(b) != "mystery-file" {
			t.Fatal("the mystery file must stay untouched")
		}
	})
	t.Run("failed staging probe leaves the existing CLI in place", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		dest := filepath.Join(m.DataRoot, "bin", cliBinary())
		if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(dest, []byte("old"), 0700); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpBroken: true, destVer: "1.0.0"})
		if err = m.installBootstrapCLI(context.Background(), &payload); err == nil {
			t.Fatal("a failing staging probe must abort the install")
		}
		if b, _ := os.ReadFile(dest); string(b) != "old" {
			t.Fatal("existing CLI must survive a failed install attempt")
		}
		entries, _ := os.ReadDir(filepath.Dir(dest))
		if len(entries) != 1 {
			t.Fatalf("staging leftovers after failure: %v", entries)
		}
	})
}

func TestPublishRuntimeStagingAndPromotion(t *testing.T) {
	buildPayload(t, "1.2.3")
	payload, err := readBootstrapPayload()
	if err != nil {
		t.Fatal(err)
	}
	meta := payload.runtimeMetadata()

	t.Run("payload copy verifies and publishes", func(t *testing.T) {
		m := newInstallTestManager(t)
		if err = m.publishRuntime(meta, stageBootstrapRuntime(payload.Dir)); err != nil {
			t.Fatalf("publish failed: %v", err)
		}
		if err = verifyRuntime(m.runtimeDir(meta.RuntimeVersion), meta.BinaryHash); err != nil {
			t.Fatalf("published dir must verify: %v", err)
		}
		if _, err = os.Stat(filepath.Join(m.Root, "runtime.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("publishRuntime must not write the version pointer")
		}
	})
	t.Run("existing version dir is reused, not merged", func(t *testing.T) {
		m := newInstallTestManager(t)
		dest := m.runtimeDir(meta.RuntimeVersion)
		if err = os.MkdirAll(dest, 0700); err != nil {
			t.Fatal(err)
		}
		if err = copyTree(filepath.Join(payload.Dir, bootstrapRuntimeDir), dest); err != nil {
			t.Fatal(err)
		}
		stray := filepath.Join(dest, "stray-user-file")
		if err = os.WriteFile(stray, []byte("user data"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = m.publishRuntime(meta, stageBootstrapRuntime(payload.Dir)); err != nil {
			t.Fatalf("reuse verification failed: %v", err)
		}
		if _, err = os.Stat(stray); err != nil {
			t.Fatal("existing version dir must never be merged or cleaned")
		}
	})
	t.Run("interrupted fill publishes nothing", func(t *testing.T) {
		m := newInstallTestManager(t)
		err = m.publishRuntime(meta, func(stage string) error {
			return errors.New("interrupted mid-copy")
		})
		if err == nil {
			t.Fatal("interrupted fill must fail")
		}
		if _, err = os.Stat(m.runtimeDir(meta.RuntimeVersion)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("interrupted install must not publish the version dir")
		}
		entries, _ := os.ReadDir(filepath.Join(m.Root, "runtime"))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".stage-") {
				t.Fatalf("staging leftovers after interruption: %s", e.Name())
			}
		}
	})
	t.Run("tampered tree fails verification and publishes nothing", func(t *testing.T) {
		m := newInstallTestManager(t)
		tampered := t.TempDir()
		if err = copyTree(filepath.Join(payload.Dir, bootstrapRuntimeDir), tampered); err != nil {
			t.Fatal(err)
		}
		notices := filepath.Join(tampered, "core-notices", "NOTICES.txt")
		if err = os.WriteFile(notices, []byte("tampered\n"), 0600); err != nil {
			t.Fatal(err)
		}
		// The tampered tree replaces the payload runtime dir wholesale.
		err = m.publishRuntime(meta, func(stage string) error { return copyTree(tampered, stage) })
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("tampered tree must fail verification, got %v", err)
		}
		if _, err = os.Stat(m.runtimeDir(meta.RuntimeVersion)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed verification must not publish")
		}
	})
}

// fakeGH rewrites every request to the test server so the production fetch
// code runs unmodified against fixture release metadata and assets.
type fakeGH struct{ server string }

func (f fakeGH) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(f.server, "http://")
	return http.DefaultTransport.RoundTrip(req)
}

func buildTarGz(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: rel, Typeflag: tar.TypeDir, Mode: int64(info.Mode().Perm())})
		}
		if err = tw.WriteHeader(&tar.Header{Name: rel, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Size: info.Size()}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestInstallRuntimeWithPayload(t *testing.T) {
	t.Run("offline first launch installs CLI, Runtime and PATH", func(t *testing.T) {
		buildPayload(t, "1.2.3")
		payload, err := readBootstrapPayload()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		t.Setenv("HOME", t.TempDir())
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destAbsent: true})
		if err = m.InstallRuntime(false, false); err != nil {
			t.Fatalf("offline first launch failed: %v", err)
		}
		installed, err := m.Installed()
		if err != nil || installed.Version != "1.0.21" || installed.SHA256 != payload.Manifest.Runtime.SHA256 {
			t.Fatalf("version pointer wrong: %+v err=%v", installed, err)
		}
		cli := filepath.Join(m.DataRoot, "bin", cliBinary())
		if hash, err := fileHash(cli); err != nil || hash != payload.Manifest.CLI.SHA256 {
			t.Fatalf("CLI not installed from payload: %v", err)
		}
		if runtime.GOOS != "windows" {
			profile, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".zprofile"))
			if err != nil || !strings.Contains(string(profile), shellPathBegin) {
				t.Fatalf("shell PATH block missing: %v", err)
			}
		}
	})
	t.Run("external runtime is not taken over", func(t *testing.T) {
		buildPayload(t, "1.2.3")
		m := newInstallTestManager(t)
		m.Endpoint = newFakeRuntimeServer(t, "/Library/OtherCLI/manage/rsrs-runtime", "").URL
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destAbsent: true})
		err := m.InstallRuntime(false, false)
		if err == nil || !strings.Contains(err.Error(), "未接管") {
			t.Fatalf("external runtime must not be taken over, got %v", err)
		}
		if _, err = os.Stat(filepath.Join(m.Root, "runtime.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("no version pointer may be written for a foreign runtime")
		}
	})
	t.Run("existing higher runtime version is not downgraded", func(t *testing.T) {
		payload, err := func() (bootstrapPayload, error) {
			buildPayload(t, "1.2.3")
			return readBootstrapPayload()
		}()
		if err != nil {
			t.Fatal(err)
		}
		m := newInstallTestManager(t)
		meta := payload.runtimeMetadata()
		previous := InstalledRuntime{Version: "1.9.9", SHA256: meta.BinaryHash}
		if err = os.MkdirAll(m.runtimeDir(previous.Version), 0700); err != nil {
			t.Fatal(err)
		}
		if err = copyTree(filepath.Join(payload.Dir, bootstrapRuntimeDir), m.runtimeDir(previous.Version)); err != nil {
			t.Fatal(err)
		}
		if err = writeJSON(filepath.Join(m.Root, "runtime.json"), previous); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destAbsent: true})
		// An existing installation checks online updates instead of reusing
		// the bundled version. A failed check must preserve the receipt.
		releaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "offline fixture", http.StatusServiceUnavailable)
		}))
		defer releaseServer.Close()
		m.HTTPClient = &http.Client{Transport: fakeGH{server: releaseServer.URL}}
		if err = m.InstallRuntime(false, false); err == nil {
			t.Fatal("offline release check cannot succeed")
		}
		current, readErr := m.Installed()
		if readErr != nil || current.Version != "1.9.9" {
			t.Fatalf("higher installed version must stay selected: %+v err=%v", current, readErr)
		}
		if _, err = os.Stat(m.runtimeDir("1.0.21")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the older payload version must not be published")
		}
		if _, err = os.Stat(filepath.Join(m.DataRoot, "bin", cliBinary())); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a failed online update must not replace the CLI from the bundle")
		}
	})
	t.Run("PATH failure keeps components and surfaces guidance", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows PATH is configured by the per-user installer")
		}
		if os.Geteuid() == 0 {
			t.Skip("permission-based failure needs a non-root runner")
		}
		buildPayload(t, "1.2.3")
		m := newInstallTestManager(t)
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.Chmod(home, 0555); err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "1.2.3", destAbsent: true})
		err := m.InstallRuntime(false, false)
		if err == nil || !strings.Contains(err.Error(), "zprofile") {
			t.Fatalf("PATH failure must surface manual guidance, got %v", err)
		}
		if installed, readErr := m.Installed(); readErr != nil || installed.Version != "1.0.21" {
			t.Fatalf("verified runtime must be kept: %+v err=%v", installed, readErr)
		}
		if _, err = os.Stat(filepath.Join(m.DataRoot, "bin", cliBinary())); err != nil {
			t.Fatal("verified CLI must be kept")
		}
	})
	t.Run("without payload the online path still installs", func(t *testing.T) {
		t.Setenv(bootstrapEnvOverride, filepath.Join(t.TempDir(), "absent"))
		m := newInstallTestManager(t)
		target, err := runtimeTarget()
		if err != nil {
			t.Fatal(err)
		}
		tree := t.TempDir()
		binaryHash := buildRuntimeTree(t, tree, target)
		archive := buildTarGz(t, tree)
		archiveHash := sha256Hex(t, archive)
		meta := RuntimeMetadata{
			Schema: 2, RuntimeVersion: "2.0.0-dev.9", Tag: "v2.0.0-dev.9", Target: target,
			ArchiveFile: runtimeArchive(target), ArchiveHash: archiveHash,
			Binary: "rsrs-runtime-" + target + binaryExtension(), BinaryHash: binaryHash,
			Compat: Compat{MinCLI: "2.0.0-dev.1", MaxCLI: "*"},
		}
		buildJSON, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		cliBytes, err := os.ReadFile(filepath.Join(tree, "rsrs-runtime-"+target))
		if err != nil {
			t.Fatal(err)
		}
		cliMeta := frontendMetadataFixture(target)
		cliMeta["binary_sha256"] = sha256Hex(t, cliBytes)
		cliMeta["binary_size"] = len(cliBytes)
		cliJSON, err := json.Marshal(cliMeta)
		if err != nil {
			t.Fatal(err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "2.0.0-dev.1", destAbsent: true})
		prefix := "https://github.com/risense-ai/respire-runtime/releases/download/" + meta.Tag + "/"
		assets := []map[string]string{
			{"name": "runtime-build-" + target + ".json", "browser_download_url": prefix + "runtime-build-" + target + ".json"},
			{"name": meta.Binary, "browser_download_url": prefix + meta.Binary},
			{"name": meta.ArchiveFile, "browser_download_url": prefix + meta.ArchiveFile},
		}
		list, err := json.Marshal([]map[string]any{{"tag_name": meta.Tag, "draft": false, "prerelease": true, "assets": assets}})
		if err != nil {
			t.Fatal(err)
		}
		cliPrefix := "https://github.com/risense-ai/respire-cli/releases/download/v2.0.0-dev.1/"
		cliAssets := []map[string]string{
			{"name": "cli-build-" + target + ".json", "browser_download_url": cliPrefix + "cli-build-" + target + ".json"},
			{"name": cliMeta["binary_file"].(string), "browser_download_url": cliPrefix + cliMeta["binary_file"].(string)},
		}
		cliList, err := json.Marshal([]map[string]any{{"tag_name": "v2.0.0-dev.1", "prerelease": true, "assets": cliAssets}})
		if err != nil {
			t.Fatal(err)
		}
		retainedMeta := meta
		retainedMeta.RuntimeVersion, retainedMeta.Tag = "2.0.0-dev.10", "v2.0.0-dev.10"
		retainedMeta.Compat.MinCLI = "2.0.0-dev.2"
		retainedJSON, err := json.Marshal(retainedMeta)
		if err != nil {
			t.Fatal(err)
		}
		retainedMetadataName := "runtime-build-" + target + ".json"
		retainedMetadataPath := "/risense-ai/respire-runtime/releases/download/" + retainedMeta.Tag + "/" + retainedMetadataName
		retainedReleaseJSON, err := json.Marshal(ReleaseInfo{Tag: retainedMeta.Tag, Prerelease: true,
			Assets: []ReleaseAsset{{Name: retainedMetadataName, URL: "https://github.com" + retainedMetadataPath}}})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.URL.Path
			switch {
			case name == "/repos/risense-ai/respire-cli/releases" && r.URL.Query().Get("per_page") == "20":
				_, _ = w.Write(cliList)
			case name == "/repos/risense-ai/respire-runtime/releases" && r.URL.Query().Get("per_page") == "20":
				_, _ = w.Write(list)
			case name == "/repos/risense-ai/respire-runtime/releases/tags/"+retainedMeta.Tag:
				_, _ = w.Write(retainedReleaseJSON)
			case name == retainedMetadataPath:
				_, _ = w.Write(retainedJSON)
			case strings.HasSuffix(name, "cli-build-"+target+".json"):
				_, _ = w.Write(cliJSON)
			case strings.HasSuffix(name, cliMeta["binary_file"].(string)):
				_, _ = w.Write(cliBytes)
			case strings.HasSuffix(name, "runtime-build-"+target+".json"):
				_, _ = w.Write(buildJSON)
			case strings.HasSuffix(name, meta.Binary):
				_, _ = w.Write(cliBytes)
			case strings.HasSuffix(name, meta.ArchiveFile):
				_, _ = w.Write(archive)
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		m.HTTPClient = &http.Client{Transport: fakeGH{server: srv.URL}}
		if err = m.InstallRuntime(false, false); err != nil {
			t.Fatalf("online pair install failed: %v", err)
		}
		installed, err := m.Installed()
		if err != nil || installed.Version != meta.RuntimeVersion || installed.SHA256 != binaryHash {
			t.Fatalf("online install pointer wrong: %+v err=%v", installed, err)
		}
		cli := filepath.Join(m.DataRoot, "bin", cliBinary())
		if hash, err := fileHash(cli); err != nil || hash != cliMeta["binary_sha256"] {
			t.Fatalf("online CLI bytes wrong: %s %v", hash, err)
		}
		entries, err := filepath.Glob(filepath.Join(m.Root, ".cli-download-*"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("CLI download staging leftovers: %v %v", entries, err)
		}
		if err = os.Remove(cli); err != nil {
			t.Fatal(err)
		}
		m.Endpoint = newFakeRuntimeServer(t, filepath.Join(m.runtimeDir(meta.RuntimeVersion), runtimeBinary()), "").URL
		if err = m.InstallRuntime(false, false); err != nil {
			t.Fatalf("online CLI repair with existing Runtime failed: %v", err)
		}
		if hash, err := fileHash(cli); err != nil || hash != cliMeta["binary_sha256"] {
			t.Fatalf("online CLI was not restored: %s %v", hash, err)
		}
		if err = copyTree(m.runtimeDir(meta.RuntimeVersion), m.runtimeDir(retainedMeta.RuntimeVersion)); err != nil {
			t.Fatal(err)
		}
		retained := InstalledRuntime{Version: retainedMeta.RuntimeVersion, SHA256: binaryHash}
		if err = writeJSON(filepath.Join(m.Root, "runtime.json"), retained); err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(cli); err != nil {
			t.Fatal(err)
		}
		retainedExe := filepath.Join(m.runtimeDir(retained.Version), runtimeBinary())
		ownedEndpoint := newFakeRuntimeServer(t, retainedExe, "").URL
		m.Endpoint = ownedEndpoint
		retainedMeta.BinaryHash = strings.Repeat("0", 64)
		retainedJSON, err = json.Marshal(retainedMeta)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.InstallRuntime(false, false); err == nil || !strings.Contains(err.Error(), "receipt") {
			t.Fatalf("retained metadata must match its own receipt: %v", err)
		}
		retainedMeta.BinaryHash = binaryHash
		retainedJSON, err = json.Marshal(retainedMeta)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.InstallRuntime(false, false); err == nil || !strings.Contains(err.Error(), "低于 Runtime") {
			t.Fatalf("retained Runtime must enforce its stricter CLI minimum: %v", err)
		}
		if _, err = os.Stat(cli); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("incompatible CLI must not be published for the retained Runtime")
		}
		// The older incoming release now rejects this CLI; only the retained
		// Runtime's own compatibility window permits the repair below.
		meta.Compat.MinCLI = "2.0.0-dev.2"
		retainedMeta.Compat.MinCLI = "2.0.0-dev.1"
		buildJSON, err = json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		retainedJSON, err = json.Marshal(retainedMeta)
		if err != nil {
			t.Fatal(err)
		}
		for _, guard := range []struct{ exe, managedID, reason string }{
			{"/other/rsrs-runtime", "", "未接管"},
			{retainedExe, "cli-supervisor", "监督器"},
		} {
			m.Endpoint = newFakeRuntimeServer(t, guard.exe, guard.managedID).URL
			if err = m.InstallRuntime(false, false); err == nil || !strings.Contains(err.Error(), guard.reason) {
				t.Fatalf("retained Runtime ownership guard failed: %v", err)
			}
			if _, err = os.Stat(cli); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("ownership guard must run before CLI publication")
			}
		}
		busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"server":"respire","v":1,"pid":4242,"exe":%q,"rpc":{"executing_until_reply":1,"queued_or_cancelled_pending_removal":0}}`, retainedExe)
		}))
		defer busy.Close()
		m.Endpoint = busy.URL
		if err = m.InstallRuntime(true, false); err == nil || !strings.Contains(err.Error(), "正忙") {
			t.Fatalf("retained Runtime busy guard failed: %v", err)
		}
		if _, err = os.Stat(cli); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("busy Runtime must defer CLI publication")
		}
		m.Endpoint = ownedEndpoint
		if err = m.InstallRuntime(false, false); err != nil {
			t.Fatalf("newer retained Runtime must still acquire its missing CLI: %v", err)
		}
		if current, readErr := m.Installed(); readErr != nil || current != retained {
			t.Fatalf("CLI repair must not downgrade Runtime: %+v %v", current, readErr)
		}
		if hash, err := fileHash(cli); err != nil || hash != cliMeta["binary_sha256"] {
			t.Fatalf("missing CLI was not installed for the retained Runtime: %s %v", hash, err)
		}
		stubCLIProbe(t, cliProbeFixture{tmpVersion: "2.0.0-dev.1", destVer: "2.0.0-dev.3"})
		retainedMeta.Compat.MaxCLI = "2.0.0-dev.2"
		retainedJSON, err = json.Marshal(retainedMeta)
		if err != nil {
			t.Fatal(err)
		}
		if err = m.InstallRuntime(false, false); err == nil || !strings.Contains(err.Error(), "兼容上限") {
			t.Fatalf("kept newer CLI must obey the retained Runtime's maximum: %v", err)
		}
		for _, failure := range []string{"hash", "size", "compat"} {
			cliMeta["binary_sha256"] = sha256Hex(t, cliBytes)
			cliMeta["binary_size"] = len(cliBytes)
			meta.Compat.MinCLI = "2.0.0-dev.1"
			switch failure {
			case "hash":
				cliMeta["binary_sha256"] = strings.Repeat("0", 64)
			case "size":
				cliMeta["binary_size"] = len(cliBytes) + 1
			case "compat":
				meta.Compat.MinCLI = "2.0.0-dev.2"
			}
			cliJSON, err = json.Marshal(cliMeta)
			if err != nil {
				t.Fatal(err)
			}
			buildJSON, err = json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			rejected := newInstallTestManager(t)
			rejected.HTTPClient = m.HTTPClient
			if err = rejected.InstallRuntime(false, false); err == nil {
				t.Fatalf("invalid CLI %s must reject the pair", failure)
			}
			if _, readErr := rejected.Installed(); !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("invalid CLI %s published a Runtime selection: %v", failure, readErr)
			}
			if _, statErr := os.Stat(filepath.Join(rejected.DataRoot, "bin", cliBinary())); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("invalid CLI %s was published: %v", failure, statErr)
			}
			entries, globErr := filepath.Glob(filepath.Join(rejected.Root, ".cli-download-*"))
			if globErr != nil || len(entries) != 0 {
				t.Fatalf("failed CLI %s left download staging: %v %v", failure, entries, globErr)
			}
		}
	})
}

func sha256Hex(t *testing.T, b []byte) string {
	t.Helper()
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// newFakeRuntimeServer answers /api/health with a minimal valid health
// identity for the ownership checks.
func newFakeRuntimeServer(t *testing.T, exe, managedID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"server":"respire","v":1,"pid":4242,"exe":%q,"managed_id":%q}`, exe, managedID)
	}))
	t.Cleanup(srv.Close)
	return srv
}
