# Foundation verification

Verified locally on Windows amd64, 2026-10-29. This describes the foundation release, not the pending full-client migration.

| Check | Result |
| --- | --- |
| Release build, `go vet`, frontend syntax | Passed; one GUI EXE, Windows icon/version manifest and SHA256SUMS |
| Supplied icon and both layouts | Passed in a real Wails WebView; compact selection survived reload |
| Native tray | Present according to Windows `Shell_NotifyIconGetRect` |
| Close to tray | Window became hidden; application stayed alive |
| Single instance | Second process exited and brought the existing window back |
| Launch-at-login support | Existing Wails Windows autostart tests passed using isolated test registry keys |
| Runtime installation | Official Windows EXE and matching native archive downloaded, checked and started in an isolated profile |
| RPC | Real health and `cli.exec` status requests succeeded |
| Runtime lifecycle | Graceful stop and subsequent restart succeeded |
| CLI Stable/Dev | Stable 1.0.16 installation and upgrade to 1.0.17-dev.14 succeeded |
| Background updates | An isolated GUI automatically upgraded its managed Runtime and staged a client update |
| Client self-update | Bad SHA-256 rejected; valid EXE replaced verification client 0.1.0 and restarted as 0.1.1 |
| Runtime continuity across client update | Runtime remained connected as 1.0.17-dev.14 |
| Input rejection | Invalid channel and API requests without the required header were rejected |
| Existing updater tests | Wails updater and GitHub-provider tests passed |

Client self-update verification used a local loopback release fixture and Go build overlays. The overlays and debugger port are under ignored `.verification/`; production code uses the official GitHub repositories and does not ship a fixture URL, debug port or bypass for checksum verification.

Real Windows sign-out/sign-in, Windows SmartScreen reputation, publisher signatures and a fault-injected Runtime rollback were not verified. The application does not force-kill an unresponsive Runtime. Binary recovery is not a database-schema rollback guarantee. Public CI/release results must be checked independently after publishing.
