# Platform support

The first release targets x64 and ARM64 on Windows, macOS and Linux. Artifact architecture names use Go names: `amd64` = x64 and `arm64` = AArch64. Additional architectures are deferred until their desktop dependencies and matching CLI/Core packages are verified.

## Build matrix

| OS | Architectures | Build method | Distribution |
| --- | --- | --- | --- |
| Windows | amd64, arm64 | Native Windows runners | One `.exe` plus one `-setup.exe` installer per architecture |
| macOS | amd64, arm64 | macOS runner with Xcode | `.zip` containing one `Respire.app`; plus `respire-darwin-arm64.dmg`, the Apple Silicon full installer with a bundled CLI and Runtime |
| Linux | amd64, arm64 | Native Ubuntu 22.04 runners | `.tar.gz` containing one `respire` |

The workflow is the source of build status. A green build verifies compilation and packaging; it does not verify every desktop environment or CPU. Windows amd64 has local interactive verification. Non-Windows UI, tray, login integration and self-update require native acceptance before a production-support claim.

## Windows installer

Each Windows architecture also ships a `respire-windows-<arch>-setup.exe` built with Inno Setup (`scripts/setup.iss`). Install-time scope selection: per-user (default, no elevation, in-app updates work) or per-machine (Program Files, elevation). The installer creates Start-menu and desktop shortcuts plus an uninstall entry, installs Microsoft Edge WebView2 when missing via the official bootstrapper (an offline failure does not abort the install; the finish page links the official download), places a bundled `rsrs.exe` into `%USERPROFILE%\.rsrs\bin` without ever downgrading an existing newer CLI, pre-stages the bundled managed Runtime into `~/.rsrs/client2/runtime/<version>/` with its `runtime.json` state so the first launch needs no download, and can append the bin folder to the user PATH (checkbox, on by default). Uninstalling removes program files, shortcuts and the PATH entry the installer added; `~/.rsrs` user data — CLI, managed Runtime and its state included — is never touched, and later Runtime updates keep flowing through the app's own channel.

CLI and Runtime are bundled from the same release channel, pinned at build time and verified against the published `cli-build-<triple>.json` / `runtime-build-<triple>.json` sha256 metadata; the build asserts the bundled CLI version sits inside the Runtime compat range and fails on a missing target, hash mismatch or an empty stable channel rather than silently substituting a prerelease. `scripts/build-installer.ps1 -Channel dev` produces a dev-channel installer. Installers are not code-signed, so SmartScreen shows an "unknown publisher" warning on first run. The bare `.exe` stays supported for portable use (first-run Runtime download unchanged), and npm's `rsrs-cli` installs to the same location - the last installation wins. Per-machine installs cannot apply in-app updates because the app directory is not writable; the client detects this with a write probe and directs the user to run the latest setup.exe.

## Deferred architectures

- Windows x86: Wails 3.0.0 compiled but panicked during COM callback initialization in an actual `--version` launch. It is not a usable artifact and is excluded.
- Linux x86/MIPS32: the unmodified Wails GTK3 bridge fails to compile because its C size typedefs differ on 32-bit targets. A local correction compiled, but maintaining a framework fork is outside this foundation release; that correction was removed.
- Linux MIPS64: a cross build succeeded, but matching CLI/Core packages and native GUI acceptance are missing. It is not shipped as supported.
- Linux LoongArch64: a compile probe succeeded with installed target libraries, while an isolated target-root build hit loader-path issues. Matching CLI/Core packages and native GUI acceptance are missing. It is deferred.

MIPS and LoongArch64 requests are Linux-only. Any later MIPS support must specify endianness, word size and floating-point ABI. Build scripts reject architectures outside the release matrix.

## Runtime prerequisites

- Windows: Windows 10+ with the corresponding WebView2 Runtime. An ARM64 build does not contain an x64 CLI fallback.
- macOS: macOS 12+ for the pinned Go 1.26 toolchain. Native oldest-supported-OS acceptance is still needed. App bundles and the DMG currently have ad-hoc signatures, without Developer ID signing or notarization; Gatekeeper therefore warns on first open of the DMG or app, and the documentation says so instead of bypassing the check. See [Go's platform requirements](https://go.dev/doc/go1.26#darwin).
- macOS (Apple Silicon full installer): `respire-darwin-arm64.dmg` carries a build-time-verified CLI and managed Runtime for `aarch64-apple-darwin`; first launch installs them into `~/.rsrs` (offline, with hash verification) and adds an owned PATH block to `~/.zprofile` for new zsh terminals. Already-installed newer versions are never downgraded, and deleting the app removes nothing from `~/.rsrs`; the README documents manual cleanup. The Intel zip ships without a runnable Runtime bundle, and the stable DMG build fails on purpose until the runtime stable train publishes.
- Linux: graphical session with GTK 3, WebKitGTK 4.1, X11 client libraries, `fontconfig` (`fc-list` for system font selection), and the libc/CPU baseline of Ubuntu 22.04. These are GNU libc builds, not Alpine/musl binaries. Do not copy libc or WebKit between incompatible distributions.
- Linux Runtime stop/update requires kernel 5.3+ for a process-bound pidfd wait. Tray visibility depends on the desktop's StatusNotifierItem support; GNOME may need an AppIndicator extension.

Typical Debian/Ubuntu native-build dependencies:

```sh
sudo apt-get install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev fontconfig
```

## CLI/Core availability

Desktop and CLI releases are separate. As inspected on 2026-10-29:

| CLI channel | Published matching targets relevant to this client |
| --- | --- |
| Stable 1.0.18 | Windows amd64/arm64, macOS arm64, Linux amd64/arm64 GNU |
| Dev 1.0.18-dev.1 | The same relevant targets as Stable 1.0.18 |

The [1.0.18 CLI release](https://github.com/risense-ai/respire-cli/releases/tag/v1.0.18) includes complete Windows arm64 and Linux arm64 packages in Stable. macOS amd64 still requires a matching CLI/Core release. Deferred x86/MIPS/LoongArch64 packages are tracked as later work in [CLI issue #163](https://github.com/risense-ai/respire-cli/issues/163), alongside the release contract and acceptance requirements.

Until then, the desktop reports the missing target and can connect to an already running compatible local Runtime. It never downloads another architecture to hide the gap. RPC remains loopback-only; this client does not add remote-Runtime access.
