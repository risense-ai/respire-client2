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

The in-progress port is not part of the foundation release. Do not label migrated views as verified until their real RPC, error paths and native integration have been exercised.
