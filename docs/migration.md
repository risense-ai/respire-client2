# Legacy-client migration acceptance

Reference: `risense-ai/respire-client`, including its React workspace, native command bridge and existing tests. The current Dashboard remains the visual reference. A button or a successful build alone does not count as functional parity.

## Remaining work

| Area | Required behavior | Verification |
| --- | --- | --- |
| Reading/appearance | System fonts, UI font and scale, reading font/size/line height; persistence and reset | Change each setting, reload, inspect rendered styles and minimum window layouts |
| Tree interaction | Keyboard navigation, create subtree, drag/drop with cycle rejection, node links, contextual actions | Real Runtime parent readback; text inputs keep normal editing shortcuts |
| Undo | Eligible create/edit/move/delete undo; reject undo after conflicting changes | Read back the restored entry and relationships; unknown writes are never replayed |
| Graph | Loaded tree and relationship graph, selection, zoom/pan and search | Cyclic/incomplete input remains bounded; graph selection opens the actual memory |
| Memory tools | Permanent purge, book/portrait material, subtree installation material, advanced filters and recall attribution | Real envelopes and explicit destructive confirmation; no demo results |
| Accounts/recovery | Password registration/login, existing-session resume, recovery-material acknowledgement, key recovery/reset and full logout | Isolated credentials and fixture service; secrets excluded from logs, journal and command line |
| Spaces | Read-only invitation, member listing/revocation and safe profile deletion | Fixture service and isolated profile; main profile cannot be deleted as a space |
| Access | Direct/inherited read-only subtree grants, revoke and material export | Actual grant contract and scope readback; no fictional external-tool installation |
| Configuration | Data-directory switching, source profiles and advanced runtime options exposed by the old client | Explicit switch, retain prior data, verify active Runtime profile and restart behavior |
| Organization | Bulk suggestion preview/application, editable plans, service selection and material export | Preview makes no writes; detect stale plans; show partial failures accurately |

## Release gate

Local evidence is recorded in [verification](verification.md). Account transport is implemented in CLI PR #170; a compatible published CLI package and native CI are still required. Live cloud/native credential-store acceptance is not implied by the isolated failure-path probes.

1. Map every old native command and visible action to an implemented action, or document a retired interface with current CLI evidence. Demo-only behavior is identified explicitly and is never presented as a working integration.
2. Keep frontend features in separate modules and Go command allowlists grouped by domain. First-party Go imports use `respire-client2/...`.
3. Use existing tests and isolated end-to-end checks; do not add repository tests without user request. Never test against the personal memory library or overwrite its keyring credentials.
4. Validate Dashboard styling in light/dark themes, sidebar/compact layouts, the minimum window, keyboard focus and dialogs.
5. Verify applicable RPC operations, error states, request recovery, process/secret handling, frontend syntax, Go static analysis and all six native CI builds.
6. Publish the next Dev release only after the migration checks pass. Describe native/cloud/hardware acceptance limits accurately; do not claim that CI proves those integrations.
