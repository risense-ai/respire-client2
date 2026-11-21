package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

// The bootstrap payload is the read-only component set a full DMG build
// embeds in Respire.app/Contents/Resources/bootstrap: bootstrap.json
// (normalized embedded-install manifest), the CLI binary, and the expanded,
// build-verified Runtime tree. First launch copies these into the standard
// ~/.rsrs layout through the regular Runtime prepare flow; nothing in the
// bundle is ever executed in place.

const (
	bootstrapManifestFile = "bootstrap.json"
	bootstrapRuntimeDir   = "runtime"
	// bootstrapEnvOverride points discovery at a fixture payload for tests
	// and headless diagnostics; it only ever affects the current process.
	bootstrapEnvOverride = "RSRS_BOOTSTRAP_DIR"
)

var errBootstrapAbsent = fmt.Errorf("bootstrap payload absent: %w", os.ErrNotExist)

type bootstrapManifest struct {
	Schema  int    `json:"schema_version"`
	Target  string `json:"target"`
	Channel string `json:"channel"`
	CLI     struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
	} `json:"cli"`
	Runtime struct {
		Version string `json:"version"`
		SHA256  string `json:"sha256"`
		MinCLI  string `json:"min_cli"`
		MaxCLI  string `json:"max_cli"`
	} `json:"runtime"`
}

type bootstrapPayload struct {
	Dir      string
	Manifest bootstrapManifest
	Metadata RuntimeMetadata
}

// bootstrapDir locates the embedded payload: an env override for tests and
// diagnostics, otherwise the app bundle's Resources directory. A build
// without a payload reports os.ErrNotExist so callers keep the online path.
func bootstrapDir() (string, error) {
	if override := os.Getenv(bootstrapEnvOverride); override != "" {
		return override, nil
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return "", errBootstrapAbsent
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(exe), "..", "Resources", "bootstrap")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(filepath.Dir(exe), "bootstrap")
	}
	if _, err = os.Stat(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errBootstrapAbsent
		}
		return "", err
	}
	if _, err = os.Stat(filepath.Join(dir, bootstrapManifestFile)); err != nil {
		return "", err
	}
	return dir, nil
}

// readBootstrapPayload discovers the embedded payload and verifies it in
// full: manifest identity for this platform, CLI hash and architecture, and
// the complete Runtime tree against the core-runtime manifest. Only an absent
// payload can use online installation; verification errors surface.
func readBootstrapPayload() (bootstrapPayload, error) {
	var payload bootstrapPayload
	dir, err := bootstrapDir()
	if err != nil {
		return payload, err
	}
	b, err := os.ReadFile(filepath.Join(dir, bootstrapManifestFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, dirErr := os.Stat(dir); errors.Is(dirErr, os.ErrNotExist) {
				return payload, errBootstrapAbsent
			} else if dirErr != nil {
				return payload, dirErr
			}
		}
		return payload, err
	}
	if err = parseBootstrapManifest(b, &payload.Manifest); err != nil {
		return payload, err
	}
	payload.Dir = dir
	cliPath := filepath.Join(dir, cliBinary())
	hash, err := fileHash(cliPath)
	if err != nil {
		return payload, err
	}
	if hash != payload.Manifest.CLI.SHA256 {
		return payload, errors.New("embedded CLI hash mismatch")
	}
	if err = verifyExecutable(cliPath); err != nil {
		return payload, fmt.Errorf("embedded CLI architecture mismatch: %w", err)
	}
	if err = verifyRuntime(filepath.Join(dir, bootstrapRuntimeDir), payload.Manifest.Runtime.SHA256, payload.Manifest.Target); err != nil {
		return payload, fmt.Errorf("embedded Runtime verification failed: %w", err)
	}
	if payload.Metadata, err = payload.runtimeMetadata(); err != nil {
		return payload, err
	}
	return payload, nil
}

func parseBootstrapManifest(b []byte, manifest *bootstrapManifest) error {
	// Unknown fields reject the manifest: the payload and the client ship in
	// the same DMG, so a mismatch means corruption, not forward compatibility.
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(manifest); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing bootstrap manifest data")
	}
	target, err := cliTarget()
	if err != nil {
		return err
	}
	if manifest.Schema != 1 {
		return fmt.Errorf("unsupported bootstrap manifest schema %d", manifest.Schema)
	}
	if manifest.Target != target {
		return fmt.Errorf("embedded payload targets %s, not %s", manifest.Target, target)
	}
	if manifest.Channel != "stable" && manifest.Channel != "dev" {
		return errors.New("embedded payload channel invalid")
	}
	if !validVersion(manifest.CLI.Version) || !validHash(manifest.CLI.SHA256) {
		return errors.New("embedded CLI identity invalid")
	}
	if !validVersion(manifest.Runtime.Version) || !validHash(manifest.Runtime.SHA256) {
		return errors.New("embedded Runtime identity invalid")
	}
	return nil
}

// runtimeMetadata validates the embedded release proof against the manifest.
func (p *bootstrapPayload) runtimeMetadata() (RuntimeMetadata, error) {
	b, err := os.ReadFile(filepath.Join(p.Dir, "runtime-build-"+p.Manifest.Target+".json"))
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("embedded Runtime metadata: %w", err)
	}
	meta, err := NormalizeRuntimeMetadata(b)
	if err != nil {
		return RuntimeMetadata{}, fmt.Errorf("embedded Runtime metadata: %w", err)
	}
	if meta.Target != p.Manifest.Target || meta.RuntimeVersion != p.Manifest.Runtime.Version || !strings.EqualFold(meta.BinaryHash, p.Manifest.Runtime.SHA256) {
		return RuntimeMetadata{}, errors.New("embedded Runtime metadata does not match bootstrap manifest")
	}
	if _, err = runtimeReceipt(meta); err != nil {
		return RuntimeMetadata{}, fmt.Errorf("embedded Runtime metadata: %w", err)
	}
	return meta, nil
}

// BootstrapStatus is the read-only report behind the headless
// bootstrap-status diagnostic: payload discovery, installed component state,
// and shell PATH ownership. It never writes anything.
type BootstrapStatus struct {
	Target              string            `json:"target"`
	PayloadFound        bool              `json:"payload_found"`
	PayloadError        string            `json:"payload_error,omitempty"`
	Payload             *PayloadInfo      `json:"payload,omitempty"`
	InstalledRuntime    *InstalledRuntime `json:"installed_runtime,omitempty"`
	InstalledRuntimeErr string            `json:"installed_runtime_error,omitempty"`
	CLIVersion          string            `json:"cli_version,omitempty"`
	CLIPath             string            `json:"cli_path,omitempty"`
	CLIError            string            `json:"cli_error,omitempty"`
	ShellPathConfigured bool              `json:"shell_path_configured"`
}

// PayloadInfo summarizes a verified embedded payload for diagnostics.
type PayloadInfo struct {
	Dir            string `json:"dir"`
	Channel        string `json:"channel"`
	CLIVersion     string `json:"cli_version"`
	RuntimeVersion string `json:"runtime_version"`
	RuntimeSHA256  string `json:"runtime_sha256"`
}

// BootstrapStatus collects the diagnostic snapshot. ctx only drives the CLI
// version probe; nothing here touches the network or writes files.
func (m *Manager) BootstrapStatus(ctx context.Context) BootstrapStatus {
	status := BootstrapStatus{}
	if target, err := runtimeTarget(); err == nil {
		status.Target = target
	}
	if payload, err := readBootstrapPayload(); err == nil {
		status.PayloadFound = true
		status.Payload = &PayloadInfo{
			Dir:            payload.Dir,
			Channel:        payload.Manifest.Channel,
			CLIVersion:     payload.Manifest.CLI.Version,
			RuntimeVersion: payload.Manifest.Runtime.Version,
			RuntimeSHA256:  payload.Manifest.Runtime.SHA256,
		}
	} else if !errors.Is(err, errBootstrapAbsent) {
		status.PayloadError = err.Error()
	}
	if installed, err := m.Installed(); err == nil {
		status.InstalledRuntime = &installed
	} else if !errors.Is(err, os.ErrNotExist) {
		status.InstalledRuntimeErr = err.Error()
	}
	if external, err := m.External(ctx); err == nil {
		status.CLIVersion = external.Version
		status.CLIPath = external.Path
	} else if !errors.Is(err, os.ErrNotExist) {
		status.CLIError = err.Error()
	}
	if home, err := os.UserHomeDir(); err == nil {
		if b, err := os.ReadFile(filepath.Join(home, shellPathFile)); err == nil {
			status.ShellPathConfigured = strings.Contains(string(b), shellPathBegin)
		}
	}
	return status
}

// probeCLIVersion runs a candidate CLI and reports the rsrs version it
// prints. Package-level so the atomic install logic is testable without a
// real rsrs binary; production matches External()'s parsing exactly.
var probeCLIVersion = func(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	configureCommand(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("读取 CLI 版本失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] != "rsrs" || !validVersion(fields[1]) {
		return "", fmt.Errorf("CLI 版本无效: %s", strings.TrimSpace(string(out)))
	}
	return fields[1], nil
}

// installBootstrapCLI publishes the embedded CLI to <data root>/bin/rsrs
// atomically: staging file, hash and architecture checks, a --version probe,
// then a rename. An existing newer CLI is kept.
func (m *Manager) installBootstrapCLI(ctx context.Context, p *bootstrapPayload) error {
	return m.installFrontendCLI(ctx, filepath.Join(p.Dir, cliBinary()), p.Manifest.CLI.Version, p.Manifest.CLI.SHA256)
}

func (m *Manager) canReuseFrontendCLI(ctx context.Context, version, expectedHash string) (bool, error) {
	dest := filepath.Join(m.DataRoot, "bin", cliBinary())
	if _, err := os.Stat(dest); err == nil {
		existing, probeErr := probeCLIVersion(ctx, dest)
		if probeErr != nil {
			// An unreadable version cannot establish reuse. Verify the new CLI before replacing it.
			return false, ctx.Err()
		}
		if semver.Compare("v"+existing, "v"+version) > 0 {
			return true, nil
		}
		if existing == version {
			hash, err := fileHash(dest)
			if err != nil {
				return false, err
			}
			if hash == expectedHash {
				return true, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

// installFrontendCLI shares verified, atomic CLI publication across both sources.
func (m *Manager) installFrontendCLI(ctx context.Context, source, version, expectedHash string) error {
	reuse, err := m.canReuseFrontendCLI(ctx, version, expectedHash)
	if err != nil || reuse {
		return err
	}
	binDir := filepath.Join(m.DataRoot, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		return err
	}
	dest := filepath.Join(binDir, cliBinary())
	tmp, err := os.CreateTemp(binDir, ".rsrs-payload-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	src, err := os.Open(source)
	if err != nil {
		tmp.Close()
		return err
	}
	_, err = io.Copy(tmp, src)
	closeErr := src.Close()
	if closeErr2 := tmp.Close(); err == nil {
		err = closeErr2
	}
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(tmpPath, 0700); err != nil {
		return err
	}
	// Re-verify what actually landed on disk instead of trusting the
	// discovery snapshot or the copy itself.
	hash, err := fileHash(tmpPath)
	if err != nil {
		return err
	}
	if hash != expectedHash {
		return errors.New("CLI hash mismatch")
	}
	if err = verifyExecutable(tmpPath); err != nil {
		return fmt.Errorf("CLI architecture mismatch: %w", err)
	}
	installed, err := probeCLIVersion(ctx, tmpPath)
	if err != nil {
		return err
	}
	if installed != version {
		return fmt.Errorf("CLI 版本探测结果 %s 与发布版本 %s 不一致", installed, version)
	}
	return os.Rename(tmpPath, dest)
}

// stageBootstrapRuntime copies the verified payload Runtime tree into the
// staging directory; publishRuntime then re-verifies and promotes it.
func stageBootstrapRuntime(payloadDir string) func(string) error {
	return func(stage string) error {
		return copyTree(filepath.Join(payloadDir, bootstrapRuntimeDir), stage)
	}
}

// copyTree copies regular files and directories only, mapping any executable
// bit to 0700 and everything else to 0600. Links and special entries abort
// the copy, so a payload can never smuggle them into the install layout.
func copyTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("payload contains unsupported entry: %s", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if info.Mode()&0111 != 0 {
			mode = 0700
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		return err
	})
}
