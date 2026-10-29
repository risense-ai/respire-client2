# Foundation verification

## Migration after 0.2.0-dev.1

Local checks on Windows amd64, 2026-10-29, used isolated data roots and loopback fixture services. The newer CLI was built from the host-RPC branch with its pinned SDK and complete runtime manifest; its local package version is not evidence of a published compatible release.

| Check | Result |
| --- | --- |
| Builds and syntax | Windows amd64/ARM64 build and Go static analysis passed; frontend syntax passed |
| Directory metadata | New CLI returned two directory rows without content; old Dev14 remained usable through capability detection |
| Edit/delete/promote undo | Real readback restored title, original ID and parent; promotion temporarily moved the child to the top level |
| Graph | Two-node view and zoom exercised in WebView; pure-layout self-check placed 5,000 cyclic nodes without recursion |
| Filters | Type filtering hid nonmatching rows; reset restored the directory; pure tag-filter check passed |
| Grants | Created a real read-only root grant, inspected inheritance on its child, revoked it and reread `revoked: true` |
| Materials | Book material included the selected entry; portrait material returned its actual entries |
| Typography | Enumerated 784 Windows fonts; scale 125%, size 20px and line height 2.2 survived reload; computed line height was 44px |
| Theme/layout | Dark compact workspace rendered with the saved reading settings and no frontend exception |
| Host account RPC | Desktop reached the isolated TOTP service using the new CLI; service rejection appeared as an error. Dev14 showed an explicit upgrade message |
| Credential boundaries | CLI failure probe returned the matching request ID without the submitted password; unknown fields, client-only mode and unsafe remote HTTP origins were rejected. Registration retained its read-only gate |
| Existing CLI checks | Auth (8), login (2), capability/revision (8) and profile transaction (1) checks passed locally; no new repository test files were added |
| Later workspace checks | Drag/drop events moved the real entry; undo restored its parent. Cyclic moves were rejected. New-entry undo cleared its deleted reader; external revisions invalidated undo. Mouse-coordinate automation at custom zoom was unreliable, so these actions used DOM events |
| Recovery receipt | A fixture success response displayed recovery material, removed close controls, refused Escape and accepted explicit acknowledgement; this did not create a cloud account |
| Broken host | An ignored executable fixture echoed a fixture credential to stderr; the unknown-result receipt and on-disk journal excluded it |
| Deep tree | An iterative traversal rendered 5,000 cyclic nodes and preserved sibling order after reproducing a recursive stack overflow |
| Lifecycle compatibility | Updated client started/stopped both the local host-RPC CLI and published Dev14 in separate profiles. No OS startup registration was performed |
| Diary undo boundary | Two untitled trivial writes returned the same day ID and `diary_appended`; undo was refused and both markers remained unchanged. Independent important create/edit undo still succeeded |
| Native CI | All six platforms passed for PR #1, including native font enumeration; main/tag builds provide the final source and artifact evidence |
| Published CLI update | Automatic updating downloaded official CLI 1.0.18-dev.1, matched its binary SHA-256, started that version with `store_ready: true`, and exposed all four host account methods |
| Startup appearance | Reproduced a WebView2 nil dereference from runtime background-color changes during startup. After moving initial color into window options, light, dark and system/tray cold starts stayed alive without a new fatal log; dark page background remained `rgb(36, 37, 31)` |

Successful cloud registration/login/recovery, OS credential-store writes, space-member revocation against a live service and real data-directory switching have not been exercised by these local checks. Multi-client undo is guarded by revision checks, not atomic compare-and-swap. CLI publication and final release assets have independent CI/identity gates; local checks do not prove deployment. The simulated external installation/synchronization state from the old UI is deliberately represented by actual CLI material and grants.

The client probes `runtime status` before starting: compatible CLIs use explicit `runtime start`, while only a known old-command input error selects the legacy `status` startup. A CLI-supervised Runtime is stopped through its coordinator. Its system registration and executable path remain owned by the CLI, so client-driven version replacement is refused before stopping it.

Create undo requires a genuinely created, independent important memory. An append to the daily diary or a deduplicated/merged result is not undone by deleting its returned ID. Edits crossing diary or legacy importance formats are also excluded. Other undo records are invalidated when recording a supported inverse fails.

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

## Current 0.2.0-dev source

Verified locally on Windows amd64 against CLI `1.0.17-dev.14`, using a separate data directory and RPC port. Temporary key material was provided only through the verification process environment; no key generation, personal-keyring update or real-user-library mutation was performed for these checks. Existing model files were copied into the isolated profile for local inference.

| Check | Result |
| --- | --- |
| Short Go module | `module respire-client2`; first-party imports use `respire-client2/internal/...`; Windows build and static analysis passed |
| Windows ARM64 | Cross-build and `go vet` passed; execution was not tested on an ARM64 computer |
| Linux/macOS host layer | Cross-target `go vet` passed; this is not a native desktop test |
| Dashboard appearance | Embedded fonts, light/dark artwork, theme selection, compact/sidebar layout and reload persistence exercised in Wails WebView |
| Native Windows appearance | Light/dark title-bar attributes changed from 0 to 1; both small/large window icon handles changed and remained nonzero |
| Live state | Real alive/total counts, sync state, inference queues and index/task state displayed |
| Memory editing | Created a parent/child pair; edited title/body in the UI and reread the Runtime entry |
| Delete/restore | UI deletion and restoration returned the original ID and parent |
| Organization | Real suggestions, similarity report, audit and local repair preview; move preview left the store unchanged, confirmed application changed the parent |
| Merge | UI merge replaced source entries and kept the unselected child's body; its parent was promoted according to the CLI contract |
| Model/index | Index preparation completed using isolated CPU model files; a nine-round UI Benchmark returned real CPU inference timings |
| Sharing/files | Share payload displayed; JSON export produced two entries, database backup succeeded, reimport reported two duplicates and no new entries |
| Settings/accounts | Actual config, engine, keystore-configuration metadata, saved profiles, spaces and injection-target receipts parsed |
| CLI display language | UI changed `lang` in the isolated CLI configuration, preserved all other settings and rejected an invalid language |
| Asynchronous writes | Write receipts remained viewable in the operation-record page; metadata journal contained IDs/status without arguments or memory bodies |
| Recovery after client restart | Simulated a completed write with a stale running journal record; it loaded as unconfirmed, then recovered the original Runtime receipt as done |
| Rejected requests | Unknown operation/argument, unconfirmed write and Codex apply without preview revision rejected |
| Warning/error handling | CLI preview warnings remained usable; missing-model errors were shown instead of a blank failure |
| Minimum window | Eleven pages in both layouts at an 800×600 viewport: no outer/content overflow or JavaScript errors observed |

Recovery checks also confirmed that a request evicted from the Runtime's last-64 cache remains unknown without replay. A metadata journal cannot recover a receipt no longer retained by the Runtime; users must inspect the current data in that case.

No new repository tests were added. Existing static/build checks and isolated end-to-end checks were used. No live cloud account, authorization, migration, key export, space mutation, injection apply or paid AI-service execution was performed. Native file dialogs are integrated but were not automated in this pass. The small synthetic recall dataset returned no hits; the Benchmark verifies inference and the command path, not retrieval quality. Large-library behavior, GPU/NPU hardware, macOS/Linux UI and signing remain unverified. Advanced migration items are listed in [delivery scope](roadmap.md).
