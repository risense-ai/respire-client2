# Client2 DEV13 preparation

This change prepares `2.0.0-dev.13` from main `f6c7ab3`, after the published
DEV12. It does not publish a release. The reviewed host entry points are from
CLI PR #315 commit `640b856`, with its final head `766bea6` (merged as
`0d01cf89`) checked for changes to these entry points; the Runtime baseline is `2.0.0-dev.21`, using
`respire-protocol 2.0.0-dev.16` and wire version 1.

## Compatibility changes

- Configuration, agent configuration, account listing and space operations run
  through the independent CLI host, rather than the pure Runtime executor.
- Engine selection, probe and recovery use distinct child operation IDs under
  one desktop operation and one captured account/instance/generation scope.
  An unknown probe never starts a recovery write.
- Request journals retain only scope, operation IDs and allowlisted host
  transaction identity. They never retain arguments, credentials, recovery
  material, challenges or complete receipts. Recovery queries the original
  scope, including after a desktop restart; old entries without scope remain
  unconfirmed instead of borrowing the current account identity.
- Runtime result recovery accepts both `completed` and `failed`, validates the
  original response identity and retains the original envelope. Pending host
  and Runtime receipts remain unconfirmed, never successful.
- Account and key receipts with structured operation/profile identity reuse
  CLI `runtime account-result` and `runtime key-result`. Key queries require
  the original selected profile. A child model receipt cannot establish that
  the entire multi-step engine application finished.
- The automatic old-generation upgrade threshold is Runtime DEV21. Downloads
  still follow the selected component channel and verified release metadata;
  no Client2 component pin or equal-product-version requirement is introduced.

## Remaining upstream limitations

CLI `640b856` accepts only `auth.register`, `auth.login`, `auth.recover` and
`auth.reset` over the credential-safe stdin host RPC. It does not expose a
desktop account/key continuation method. Client2 therefore preserves pending
transactions, queries available original-result commands, and reports missing
input support explicitly. It never resubmits an authentication or key write.

In particular, the CLI login path can require an interactive terminal for
additional input. Its generic failure envelope does not always retain a
structured operation ID and source profile. Client2 cannot safely reconstruct
those from arbitrary error text or replace them with the desktop request ID;
such operations require CLI-side reconciliation. This PR does not modify CLI
or Runtime, and does not claim complete desktop continuation acceptance.

## Validation and release gate

Use the existing Go tests and vet with the `production` tag, frontend syntax
checks, `scripts/test-account-session-state.cjs` and
`scripts/test-client-refresh.cjs`. Do not add repository tests for this task.
PR CI runs the existing light checks; no tag or native release is requested.

Before a later DEV13 release, freeze the actual published CLI and Runtime
assets and their hashes, then run isolated native acceptance for host routing,
engine child identities, failure/pending recovery, restart scope fencing and
issued recovery-material acknowledgement. Use synthetic accounts and a
dedicated data root and credential namespace. Source CI alone does not prove
cloud login, native keyring, login autostart or end-to-end compatibility.

Local Windows amd64 validation passed: existing `go test -tags production
./...`, `go vet -tags production ./...`, all 33 frontend JavaScript syntax
checks, the existing 34 account/session and 12 refresh checks, PowerShell build
script parsing, and a production desktop build reporting `2.0.0-dev.13`.
An ignored assertion program also exercised failed original receipts,
scope preservation across a desktop restart, pending host identity and
secret-free journals, separate model child identities, and an unknown model
probe without a recovery write. These used synthetic loopback and CLI protocol
fixtures, not a real account, credential store, database or inference engine.

Runtime DEV22 publication and the next CLI pin/release are managed upstream.
Their release timing does not block this PR, and this change does not modify
either repository or claim a published new-stack acceptance result.
