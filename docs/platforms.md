# Platform support

The first release targets x64 and ARM64 on Windows, macOS and Linux. Artifact architecture names use Go names: `amd64` = x64 and `arm64` = AArch64. Additional architectures are deferred until their desktop dependencies and matching CLI/Core packages are verified.

## Build matrix

| OS | Architectures | Build method | Distribution |
| --- | --- | --- | --- |
| Windows | amd64, arm64 | Native Windows runners | One `.exe` per architecture |
| macOS | amd64, arm64 | macOS runner with Xcode | `.zip` containing one `Respire.app` |
| Linux | amd64, arm64 | Native Ubuntu 22.04 runners | `.tar.gz` containing one `respire` |

The workflow is the source of build status. A green build verifies compilation and packaging; it does not verify every desktop environment or CPU. Windows amd64 has local interactive verification. Non-Windows UI, tray, login integration and self-update require native acceptance before a production-support claim.

## Deferred architectures

- Windows x86: Wails 3.0.0 compiled but panicked during COM callback initialization in an actual `--version` launch. It is not a usable artifact and is excluded.
- Linux x86/MIPS32: the unmodified Wails GTK3 bridge fails to compile because its C size typedefs differ on 32-bit targets. A local correction compiled, but maintaining a framework fork is outside this foundation release; that correction was removed.
- Linux MIPS64: a cross build succeeded, but matching CLI/Core packages and native GUI acceptance are missing. It is not shipped as supported.
- Linux LoongArch64: a compile probe succeeded with installed target libraries, while an isolated target-root build hit loader-path issues. Matching CLI/Core packages and native GUI acceptance are missing. It is deferred.

MIPS and LoongArch64 requests are Linux-only. Any later MIPS support must specify endianness, word size and floating-point ABI. Build scripts reject architectures outside the release matrix.

## Runtime prerequisites

- Windows: Windows 10+ with the corresponding WebView2 Runtime. An ARM64 build does not contain an x64 CLI fallback.
- macOS: macOS 12+ for the pinned Go 1.26 toolchain. Native oldest-supported-OS acceptance is still needed. App bundles currently have ad-hoc signatures, without Developer ID signing or notarization. See [Go's platform requirements](https://go.dev/doc/go1.26#darwin).
- Linux: graphical session with GTK 3, WebKitGTK 4.1, X11 client libraries, and the libc/CPU baseline of Ubuntu 22.04. These are GNU libc builds, not Alpine/musl binaries. Do not copy libc or WebKit between incompatible distributions.
- Linux Runtime stop/update requires kernel 5.3+ for a process-bound pidfd wait. Tray visibility depends on the desktop's StatusNotifierItem support; GNOME may need an AppIndicator extension.

Typical Debian/Ubuntu native-build dependencies:

```sh
sudo apt-get install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev libx11-dev
```

## CLI/Core availability

Desktop and CLI releases are separate. As inspected on 2026-10-29:

| CLI channel | Published matching targets relevant to this client |
| --- | --- |
| Stable 1.0.16 | Windows amd64, macOS arm64, Linux amd64 GNU |
| Dev 1.0.17-dev.14 | The Stable targets plus Linux arm64 GNU |

Windows arm64 and macOS amd64 require additional complete CLI/Core releases for the initial desktop matrix. Linux arm64 also needs promotion into Stable. Deferred x86/MIPS/LoongArch64 packages are tracked as later work in [CLI issue #163](https://github.com/risense-ai/respire-cli/issues/163), alongside the release contract and acceptance requirements.

Until then, the desktop reports the missing target and can connect to an already running compatible local Runtime. It never downloads another architecture to hide the gap. RPC remains loopback-only; this client does not add remote-Runtime access.
