# Delivery scope

The first release is the desktop foundation for Windows, macOS and Linux. Memory-management UI migration follows it; the previous client repository is retained as a reference, not shipped as a Tauri dependency. MIPS and LoongArch64 targets are Linux-only.

## Foundation

- Wails v3, Windows single EXE, macOS app bundles, Linux executables, supplied Respire icon.
- Native tray, close-to-tray, single instance and launch-at-login registration.
- RPC health and diagnostics; managed Runtime installation, graceful lifecycle and automatic updates.
- Independent Stable/Dev update channels for CLI and desktop; no automatic downgrade.
- Sidebar and compact top-navigation layouts with persisted selection.
- Repeatable platform builds, checksums, third-party notices, CI artifacts and tagged GitHub releases.
- Matching CLI/Core packages for missing targets are tracked independently in [respire-cli#163](https://github.com/risense-ai/respire-cli/issues/163); compiling the desktop is not evidence of CLI availability.

## Full client migration

Port the existing behavior against the current CLI rather than copying obsolete API assumptions. Keep the Go command allowlist split by business area and keep frontend components separate from Runtime management.

- Memory tree, full-text/semantic search, content reading, create/edit, move/attach, causal links, deletion/restore/purge and eligible undo.
- Diary calendar and detail views; graph view; keyboard navigation and shortcuts.
- Account registration/login/offline mode, recovery material and key maintenance.
- Spaces, invitations, membership, switching and explicit destructive confirmations.
- Sync status, server settings, automatic sync, configuration and data-directory changes.
- Injection target discovery, preview/apply with revision checks, scope material and subtree sharing/import.
- Export/import, database backup, native file dialogs and clipboard.
- Appearance, theme, font enumeration, UI scale, reading font/size/line height.
- Maintenance previews, classification configuration through the CLI keystore, asynchronous result tracking, tree repair and re-embedding.

Current CLI versions retire the old optional cross-encoder installer. Preserve its honest capability state instead of restoring a nonfunctional install button. Long writes must retain their request IDs; a timeout is not permission to replay a write.

## Current 0.2.0-dev implementation

The published `v0.1.0` remains unchanged. Current source adds dashboard typography/colors, theme-aware artwork, memory browsing/editing/organization, diary views and the following TUI operations:

| TUI page | Desktop entry |
| --- | --- |
| Home / Sync | Overview: counts, synchronization, queues, model task and index state |
| Accounts / Migration | Accounts and spaces: browser login, profiles, offline initialization, explicit migration |
| Server | Accounts: server address, automatic synchronization and manual synchronization |
| Inject | Context injection: targets, apply/remove; Codex also has revision-checked file preview |
| Model | Models and index: preparation, cancel/restart, mirrors, engines, provider installation, re-embedding, recall configuration and nine-round checks |
| Workspace / Language | Settings: normal/read-only/off, diary mode and CLI language |
| Version | Updates: independent client/CLI channels, install/download and apply |
| Web | Accounts: open the hosted dashboard |

Memory operations include tree/list, full reading, semantic recall, editing, moving, merging, delete/restore, causal chains, sharing, calendar/fragments, repair previews and explicit plan application. Import/export/backup use native file pickers. Long writes keep request IDs; unknown outcomes are queried, never automatically replayed. Complete write receipts stay in memory for the current session; only metadata survives a client restart.

This is still a staged migration of the previous client. Its graph, drag operations, full keyboard/undo behavior, access-grant editor, detailed typography controls, password registration/recovery and member administration are not yet ported. Native macOS/Linux acceptance, real browser authorization/cloud synchronization and AI-service execution still need verification. See [verification](verification.md) for exercised paths; a visible entry alone is not a verified integration.
