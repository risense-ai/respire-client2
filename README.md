# Respire Client 2

A Windows desktop client built with Wails v3 and Go. It replaces the desktop application in `risense-ai/respire-client`. The CLI, TUI, hosted dashboard and existing user data remain independent.

Download one `respire-windows-amd64.exe`, keep it in a writable, permanent folder, and run it. Microsoft Edge WebView2 Runtime must be installed. No Node.js, Go, npm or Rust installation is needed by the end user. The client downloads the CLI and its native libraries separately when no Runtime is available; the EXE does not contain Core or model weights.

## Current scope

- Native tray, one application instance, close-to-tray, and launch at Windows login with `--tray`.
- Sidebar layout or compact top navigation, with the selection saved across restarts. EXE, tray and window artwork use the supplied Respire icon.
- Runtime health, queue status and diagnostic operations over loopback HTTP RPC.
- First-run Runtime download, SHA-256 validation, bounded archive extraction, complete native-library validation, start/stop and version updates.
- Client update checks and downloads through Wails' updater. A downloaded client update is applied by **Restart and apply**; updating the desktop does not stop the Runtime.
- Independent Stable / Dev channels for both CLI and client, preferences, and visible operation/error states. No memory editor or account manager is included in this first version.

## Repository structure

```text
main_windows.go                       Entry point and headless diagnostics
internal/
  desktop/
    app_windows.go                    Wails lifecycle, single instance, tray, scheduling
    api_windows.go                    In-WebView HTTP actions; no public HTTP listener
    updates_windows.go                Wails client updater and required-checksum policy
    frontend/{index.html,app.css,app.js} Embedded UI, without a frontend build dependency
    build/icon.png                    Application and tray artwork
  host/
    manager_windows.go                Installation root, preferences, atomic JSON, task state
    rpc_windows.go                    Bounded loopback transport and diagnostic commands
    lifecycle_windows.go              Verified process identity, host start and graceful stop
    releases_windows.go               Official CLI release discovery and checked downloads
    install_windows.go                Safe extraction, library validation, switch and recovery
build/winres.json                     EXE icon, version and Windows manifest
scripts/build.ps1                     Repeatable local/CI build and checksums
.github/workflows/build.yml           Windows build artifacts and tagged releases
```

Dependency direction is `main → desktop → host`. The host package does not import Wails. The frontend only invokes the desktop API; it does not execute commands, open databases, load Core or call the cloud API. Routine diagnostic requests use RPC rather than spawning a CLI. CLI subprocesses are reserved for host lifecycle actions.

Keep new Runtime operations in `host`, desktop integration in `desktop`, and rendering in `frontend`. Add narrow actions instead of an arbitrary shell/CLI execution endpoint. Keep error handling in the layer that owns the operation. Do not add a second updater, independent lifecycle coordinator, or frontend copy of Runtime business rules.

## Build

Windows amd64, Go 1.26+, Node.js 22+ (syntax checking only), PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1 -Version 0.1.0
```

The output is `dist/respire-windows-amd64.exe`, `dist/SHA256SUMS`, `dist/LICENSE.txt` and `dist/THIRD_PARTY_NOTICES.txt`. Third-party notices are also embedded in the EXE. Dependencies and build caches stay under `.tools/`. The script runs `go vet` and `node --check`, generates Windows resources, then builds a GUI subsystem executable without a console window. Do not commit caches, binaries, generated `.syso` files or verification data.

For an isolated run, use a dedicated root and an unused port:

```powershell
$env:RSRS_DATA_DIR = Join-Path $PWD '.verification\profile'
$env:RSRS_RPC_PORT = '19169'
Start-Process -FilePath (Join-Path $PWD 'dist\respire-windows-amd64.exe') -WindowStyle Hidden
```

Do not use the real user profile for installation/update verification. The first launch can download the current official CLI package. A running Runtime is intentionally left alive when the desktop exits.

## Local layout and ownership

The installation root is `~/.rsrs`, or the existing `RSRS_DATA_DIR` override. Client-owned files live under `<root>/client2/`:

```text
preferences.json          Auto-connect and auto-update preferences
runtime.json              Current managed version and executable SHA-256
runtime/<version>/        Immutable CLI, core-runtime.json, native libraries and notices
webview2/                 WebView2 browser profile
client.log                Desktop operational errors
```

`RSRS_RPC_PORT` overrides `15169`. RPC remains HTTP on `127.0.0.1`; the client never changes Runtime bind addresses, authentication policy or Origin checks. Installed CLI host actions receive `RSRS_BIN_DIR` pointing at their complete version directory so that the CLI does not materialize an EXE without its matching libraries.

An already running compatible Runtime is attached without replacement. Automatic updates only stop a Runtime whose executable is inside this client's managed directory. A Runtime started by another CLI remains under that CLI's ownership; explicitly stop it before installing a managed Runtime. Other host CLIs can still take over the shared Runtime endpoint according to their own version policy. This client does not modify those CLIs or promise exclusive ownership of port 15169.

Login startup registers the current EXE path through Wails. Moving or deleting that EXE invalidates the registration; disable and re-enable the switch after moving it. This is a per-user login entry, not a Windows system service. The window close button hides the window. The tray's exit action exits only the desktop.

## Update contract

### Desktop client

Source: GitHub releases in `risense-ai/respire-client2`. Stable (the default) uses the latest stable release; Dev includes published prereleases. Each release must contain:

```text
respire-windows-amd64.exe
SHA256SUMS
LICENSE.txt
THIRD_PARTY_NOTICES.txt
```

`SHA256SUMS` uses standard `hex-digest  filename` lines. An update with a missing or invalid SHA-256 is rejected. Wails performs download staging, checksum verification and Windows helper-based replacement/restart recovery. The helper is re-executed from the same application: no separate updater EXE is distributed. Automatic checks run after startup and every six hours; downloads are staged, then the user restarts to apply. An empty/unpublished repository cannot supply a production update and is reported in the update panel.

This initial release uses HTTPS GitHub release metadata and SHA-256, as Magpie does for Windows binary updates. It does not implement publisher-key signatures or Authenticode signing. A hash supplied by the same release source does not defend against compromise of that source. Add release signing before changing that trust assumption; do not silently bypass failed validation.

### Runtime

Source: published `risense-ai/respire-cli` releases. Stable (the default) uses GitHub's latest stable release. Dev selects the highest semantic version among the latest 20 published releases, including prereleases. The Windows amd64 release must provide:

- `cli-build-x86_64-pc-windows-msvc.json` with schema 1, version, target, source commit and both artifact hashes.
- `rsrs-x86_64-pc-windows-msvc.exe`.
- `rsrs-x86_64-pc-windows-msvc-runtime.tar.gz`, containing `core-runtime.json` and every declared native-library/notice file.

The download is staged under the managed Runtime directory. Archive links, special files, escaping paths, oversized archives and mismatched file hashes are rejected. No running EXE or DLL is overwritten. After validation, a version directory is published, the current managed Runtime is stopped gracefully, the selected-version pointer is saved atomically, and the new CLI starts through the existing host path. Its health version and executable path must match. A failed start restores the previous selected version and attempts to start it again. Older version directories are retained for recovery; cleanup is deliberately deferred.

An automatic switch is deferred while RPC work is executing or queued; the check runs after downloading, and a complete downloaded version is retained for the next attempt. The Runtime remains responsible for draining its writes and background tasks. Stop waits for the verified process to exit and never force-kills it. If a stop or restart cannot be confirmed, the error remains visible. Binary rollback does not guarantee rollback of a database schema migrated by a future CLI; such releases need a separate compatibility/migration policy before automatic distribution.

The two channel selections and the layout are independent and persist in `preferences.json`. Switching channels saves the policy; it does not interrupt an ongoing operation or automatically downgrade an installed version. If the installed Dev version is ahead of Stable, it remains installed until Stable catches up. Apply an already downloaded client update before changing its channel. Runtime channel selection is disabled during a Runtime task. New native/RPC operations must preserve these rules.

RPC currently requires protocol `v = 1`; the first version exposes only `status` and `doctor` through `cli.exec`. Capability negotiation must be added before relying on newer optional RPC methods. Write operations must retain request IDs and unknown-result handling; never replay a timed-out write automatically.

## Releases

Branch and pull-request builds upload artifacts without publishing a release. A pushed `vX.Y.Z` tag builds the same script and creates a GitHub release containing the EXE, checksum file and license. Tags such as `vX.Y.Z-dev.N` publish a prerelease for the Dev channel and are not selected by the stable updater. The release workflow uses the repository's scoped `GITHUB_TOKEN`; no account credentials belong in source.

The first release must be explicitly published before public self-update can work. Building locally does not publish, push, install a login entry or replace a user's Runtime.

## Diagnostics

The same EXE supports JSON over redirected stdout for support and automation:

```powershell
& .\dist\respire-windows-amd64.exe --headless status
& .\dist\respire-windows-amd64.exe --headless rpc-status
& .\dist\respire-windows-amd64.exe --headless runtime-release
& .\dist\respire-windows-amd64.exe --headless install-runtime
& .\dist\respire-windows-amd64.exe --headless start
& .\dist\respire-windows-amd64.exe --headless stop
```

`status`, `rpc-status` and `runtime-release` do not start or replace a Runtime. Installation/start/stop are explicit host actions; always set an isolated root and port when verifying them. Inspect `client.log` and the diagnostic panel for errors. No fallback mocks are shown in the production UI.

## References and license

See [local foundation verification](docs/verification.md) for exercised paths and limitations, and [delivery scope](docs/roadmap.md) for the full-client migration that follows this release.

Architecture and product behavior were informed by [Magpie](https://github.com/yetone/magpie); this is a new client, not a fork of Magpie's gateway. Wails provides the desktop, autostart and client-update machinery. Runtime/Core/model files keep their own redistribution notices. See [LICENSE](LICENSE) for first-party terms.
