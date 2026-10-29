# Respire Client 2

A desktop client built with Wails v3 and Go for Windows, macOS and Linux. It replaces the desktop application in `risense-ai/respire-client`. The CLI, TUI, hosted dashboard and existing user data remain independent.

Download the asset matching your OS and architecture from [Releases](https://github.com/risense-ai/respire-client2/releases). Keep the extracted application in a writable, permanent folder. Windows uses one EXE and requires Microsoft Edge WebView2. macOS uses a zipped `Respire.app`; Linux uses a tarball containing one executable and requires GTK 3, WebKitGTK 4.1 and X11 libraries. No Node.js, Go, npm or Rust installation is needed by the end user. The application downloads the CLI and its native libraries separately when available; it does not contain Core or model weights.

See [platform support](docs/platforms.md) for architectures, native-library prerequisites and the distinction between desktop builds and matching CLI availability. Missing CLI/Core release targets are tracked in [respire-cli#163](https://github.com/risense-ai/respire-cli/issues/163).

## Current scope

- Native tray, one application instance, close-to-tray, and launch at system login with `--tray`.
- Sidebar layout or compact top navigation, plus Light / Dark / System themes, saved across restarts. The supplied light/dark artwork follows the theme in the UI, tray and native window.
- Dashboard typography and paper/charcoal colors, with fonts embedded for offline use and their licenses included.
- Runtime health, memory counts, synchronization, model tasks, inference queues and retrieval-index status over loopback HTTP RPC.
- Memory tree/list, full reading, semantic recall, create/edit, move, merge, delete/restore, causal chains and subtree sharing. Merge follows the CLI contract: surviving children are promoted to surviving ancestors.
- Diary calendar and fragment search; organization suggestions, similarity checks, audit, local repair preview, validated move plans and optional AI classification.
- Model/index preparation, cancellation/restart, inference-engine selection, provider installation, recall-service configuration and local/quality recall checks with a nine-round Benchmark.
- Accounts, browser login, offline initialization, spaces, migration, synchronization settings, injection targets, native file selection and import/export/backup.
- First-run Runtime download, SHA-256 validation, bounded archive extraction, complete native-library validation, start/stop and version updates.
- Client update checks and downloads through Wails' updater. A downloaded client update is applied by **Restart and apply**; updating the desktop does not stop the Runtime.
- Independent Stable / Dev channels for both CLI and client, preferences, retained request IDs and explicit unknown-result handling.

The published `v0.1.0` remains the foundation release. The memory-management and appearance work described above is the current `0.2.0-dev` source. See [verification](docs/verification.md) and [migration scope](docs/roadmap.md) for tested paths and remaining work.

## Repository structure

```text
main.go                               Entry point and headless diagnostics
internal/
  desktop/
    app.go                            Wails lifecycle, single instance, tray, scheduling
    api.go                            In-WebView HTTP actions; no public HTTP listener
    updates.go                        Wails client updater and required-checksum policy
    appearance*.go                    Theme resolution and native window/tray artwork
    business.go, files.go              Named business actions and native file dialogs
    frontend/workspace.html           Embedded entry point; no frontend build dependency
    frontend/client.js, ui/           Routing, status polling and UI helpers
    frontend/pages/                   Runtime, account, settings and organization views
    frontend/memory/                  Tree/list, editor, diary and memory relationships
    frontend/fonts/                   Dashboard fonts and redistribution licenses
    build/icon*.png                   Light/dark native artwork
  host/
    manager.go                        Installation root, preferences, atomic JSON, task state
    rpc.go                            Bounded loopback transport and diagnostic commands
    lifecycle.go                      Host start, stop, selected version and recovery
    process_{windows,linux,darwin}.go  OS-specific process identity and exit waiting
    platform.go                       Asset names, CLI targets and PE/Mach-O/ELF validation
    releases.go                       Official CLI release discovery and checked downloads
    install.go                        Safe extraction, library validation, switch and recovery
    commands_*.go                     Explicit operation/argument allowlists by business area
    operations.go                     Asynchronous RPC requests, receipts and recovery
    host_operations.go                Verified CLI host actions; API keys passed on stdin
build/winres.json                     EXE icon, version and Windows manifest
scripts/build.ps1                     Windows x64/ARM64 build and checksums
scripts/build.sh                      Native macOS/Linux build and packaging
.github/workflows/desktop.yml         Per-platform CI artifacts and tagged releases
```

Dependency direction is `main → desktop → host`. The host package does not import Wails. The frontend only invokes named desktop actions; it does not execute commands, open databases, load Core or call the cloud API. Memory, synchronization and model operations use RPC. Verified CLI subprocesses handle operations owned by the host, including browser authorization, account switching, injection and keystore configuration.

The Go module is `respire-client2`. Import this repository's own packages as `respire-client2/internal/...`; do not use a domain-prefixed module name or `./` imports. Third-party dependencies retain their official module paths.

CLI display language is a host preference: the CLI has no language command/RPC. The desktop follows its `client.json` `lang` contract, preserves other keys and writes atomically under the selected installation root. Memory and account data are still managed by the Runtime/CLI.

Keep new Runtime operations in `host`, desktop integration in `desktop`, and rendering in `frontend`. Add narrow actions instead of an arbitrary shell/CLI execution endpoint. Keep error handling in the layer that owns the operation. Do not add a second updater, independent lifecycle coordinator, or frontend copy of Runtime business rules.

## Build

Go 1.26+, Node.js 22+ (syntax checking only). On Windows, use PowerShell:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build.ps1 -Version 0.2.0-dev.0 -Arch amd64
```

`-Arch` accepts `amd64` or `arm64`. The resource generator runs on the build machine's architecture; the application is built for the requested architecture. The output is `dist/respire-windows-<arch>.exe`, `dist/SHA256SUMS`, `dist/LICENSE.txt` and `dist/THIRD_PARTY_NOTICES.txt`.

On macOS (Xcode Command Line Tools) or Linux (C/C++ compiler, pkg-config, GTK/WebKit development libraries):

```sh
bash scripts/build.sh 0.2.0-dev.0 amd64  # or arm64 on the corresponding native runner
```

Linux builds use Wails' `gtk3` tag and link against system GTK 3 and WebKitGTK 4.1. CI builds each architecture on a native runner. macOS packaging generates the app icon and applies an ad-hoc signature, which is not an Apple Developer ID signature or notarization.

Third-party notices are embedded in the executable and copied alongside release assets. Dependencies and build caches stay under `.tools/`. Build scripts run `go vet` and frontend syntax checking when Node is available; CI's native jobs always include Node. Windows resources produce a GUI executable without a console window. Do not commit caches, binaries, generated `.syso` files or verification data.

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
requests.json             Write request IDs/status only; no arguments, keys or memory bodies
runtime.json              Current managed version and executable SHA-256
runtime/<version>/        Immutable CLI, core-runtime.json, native libraries and notices
webview2/                 WebView2 browser profile
client.log                Desktop operational errors
```

`RSRS_RPC_PORT` overrides `15169`. RPC remains HTTP on `127.0.0.1`; the client never changes Runtime bind addresses, authentication policy or Origin checks. Installed CLI host actions receive `RSRS_BIN_DIR` pointing at their complete version directory so that the CLI does not materialize an EXE without its matching libraries.

An already running compatible Runtime is attached without replacement. Automatic updates only stop a Runtime whose executable is inside this client's managed directory. A Runtime started by another CLI remains under that CLI's ownership; explicitly stop it before installing a managed Runtime. Other host CLIs can still take over the shared Runtime endpoint according to their own version policy. This client does not modify those CLIs or promise exclusive ownership of port 15169.

Login startup registers the current application through Wails: Windows per-user login, macOS login item/LaunchAgent, or Linux XDG autostart. Moving or deleting the application invalidates the registration; disable and re-enable the switch after moving it. The window close button hides the window. The tray's exit action exits only the desktop. Linux trays require desktop support for StatusNotifierItem; some GNOME installations need an AppIndicator extension.

## Update contract

### Desktop client

Source: GitHub releases in `risense-ai/respire-client2`. Stable (the default) uses the latest stable release; Dev includes published prereleases. Each release must contain:

```text
respire-windows-<arch>.exe    # Windows: amd64, arm64
respire-darwin-<arch>.zip     # macOS: amd64, arm64; one .app bundle
respire-linux-<arch>.tar.gz   # Linux: one respire executable
SHA256SUMS
LICENSE.txt
THIRD_PARTY_NOTICES.txt
```

`SHA256SUMS` uses standard `hex-digest  filename` lines for every platform asset. The updater selects exactly the running OS/architecture and rejects missing or invalid SHA-256 checksums. Wails performs download staging, archive extraction and helper-based replacement/restart recovery; macOS replaces the complete app bundle. The helper is re-executed from the same application: no separate updater is distributed. Automatic checks run after startup and every six hours; downloads are staged, then the user restarts to apply. An empty/unpublished repository cannot supply a production update and is reported in the update panel.

This initial release uses HTTPS GitHub release metadata and SHA-256. It does not implement publisher-key signatures or Authenticode signing. A hash supplied by the same release source does not defend against compromise of that source. Add release signing before changing that trust assumption; do not silently bypass failed validation.

### Runtime

Source: published `risense-ai/respire-cli` releases. Stable (the default) uses GitHub's latest stable release. Dev selects the highest semantic version among the latest 20 published releases, including prereleases. Each platform's mapped target must provide (Windows amd64 examples below):

- `cli-build-x86_64-pc-windows-msvc.json` with schema 1, version, target, source commit and both artifact hashes.
- `rsrs-x86_64-pc-windows-msvc.exe`.
- `rsrs-x86_64-pc-windows-msvc-runtime.tar.gz`, containing `core-runtime.json` and every declared native-library/notice file.

The download is staged under the managed Runtime directory. Archive links, special files, escaping paths, oversized archives and mismatched file hashes are rejected. No running EXE or DLL is overwritten. After validation, a version directory is published, the current managed Runtime is stopped gracefully, the selected-version pointer is saved atomically, and the new CLI starts through the existing host path. Its health version and executable path must match. A failed start restores the previous selected version and attempts to start it again. Older version directories are retained for recovery; cleanup is deliberately deferred.

PE, Mach-O or ELF headers must match the running OS and architecture; Unix executable permissions are preserved. A missing target is reported directly, with no substitution or emulation of a different CLI architecture. Linux lifecycle management uses pidfds and requires kernel 5.3+; macOS checks the executable path and process start time before waiting for exit.

An automatic switch is deferred while RPC work is executing or queued; the check runs after downloading, and a complete downloaded version is retained for the next attempt. The Runtime remains responsible for draining its writes and background tasks. Stop waits for the verified process to exit and never force-kills it. If a stop or restart cannot be confirmed, the error remains visible. Binary rollback does not guarantee rollback of a database schema migrated by a future CLI; such releases need a separate compatibility/migration policy before automatic distribution.

The two channel selections and the layout are independent and persist in `preferences.json`. Switching channels saves the policy; it does not interrupt an ongoing operation or automatically downgrade an installed version. If the installed Dev version is ahead of Stable, it remains installed until Stable catches up. Apply an already downloaded client update before changing its channel. Runtime channel selection is disabled during a Runtime task. New native/RPC operations must preserve these rules.

RPC requires protocol `v = 1`. Business actions use allowlisted `cli.exec` arguments; model/index preparation and recovery use the Runtime methods `model.prepare`, `model.control`, `index.prepare` and `runtime.result`. Unsupported commands and methods remain visible errors. The current business UI was exercised with CLI `1.0.17-dev.14`; do not assume the stable CLI has every optional feature.

Writes keep one request ID. A lost receipt triggers a lookup of that original request, never an automatic replay. Full write receipts remain in memory during the session; after restart only metadata is retained. The verified CLI retains its last 64 completed requests in memory; an evicted receipt or a restarted Runtime can remain unknown, which is not proof that its write did not commit. Host actions with an unknown outcome must be checked on their corresponding page because Runtime cannot recover a host-process receipt. Configuration errors and missing model files are shown directly.

## Releases

Branch and pull-request builds upload per-platform artifacts without publishing a release. A pushed `vX.Y.Z` tag runs the same matrix, combines all checksums and creates one GitHub release only after every required build succeeds. Tags such as `vX.Y.Z-dev.N` publish a prerelease for the Dev channel and are not selected by the stable updater. The release workflow uses the repository's scoped `GITHUB_TOKEN`; no account credentials belong in source.

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

Wails provides the desktop, autostart and client-update machinery. Runtime/Core/model files keep their own redistribution notices. See [LICENSE](LICENSE) for first-party terms.
