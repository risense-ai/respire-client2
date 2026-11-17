# Client2 DEV13 preparation

This change prepares `2.0.0-dev.13` from main `f6c7ab3`, after the published
DEV12. It does not publish a release. The reviewed host entry points are from
CLI PR #315 commit `640b856`, with its final head `766bea6` (merged as
`0d01cf89`) checked for changes to these entry points; the target Runtime baseline is `2.0.0-dev.23`, using
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
- The automatic old-generation upgrade threshold is Runtime DEV23. Downloads
  still follow the selected component channel and verified release metadata;
  no Client2 component pin or equal-product-version requirement is introduced.

## Explicit account/key continuation

CLI host stdio retains version 1 and advertises `auth.login.continue` and
`auth.key.continue` in `summary.capabilities.host_rpc.methods`. Client2 queries
and validates the original receipt before submitting only requested inputs.
The original desktop job, host `operation_id` and `source_profile` remain
unchanged; each submission uses an independent transport request ID.

Login continuation sends typed `{operation_id, source_profile, continuation,
super_password?, totp?}`. The continuation object contains only DEV16
supplemental fields. CLI restores the original authorization, account and
migration intent from its custody. Key continuation sends typed
`{operation_id, source_profile, input}` with only user-supplied DEV16 key inputs.
Client2 never constructs private material, challenges, custody acknowledgements
or replacement intent. A capability failure or uncertain transport result
requires another original-result query before collecting inputs again.

`runtime key-result --operation-id=... --source-profile=...` lets CLI validate
the original profile while Runtime is paused. It replaces the former live-health
precondition. Restarted clients first recover the original receipt and then show
its requested inputs in Operations. Password/browser login, key recovery and
super-password reset use the same continuation UI. Cancelling input preserves
the original job; resubmitting the original form reconciles that job instead of
starting authentication again. Only a real completed receipt reports success;
issued recovery material still requires explicit save acknowledgement.

Browser authorization uses the capability-gated `auth.login.browser` stdio
method with an empty string map or only `addr`. CLI owns OAuth and returns the
original structured receipt, so subsequent input uses `auth.login.continue`;
Client2 no longer starts browser login through the unstructured argv path.
A queried terminal failure carries its operation back to the shared auth helper,
which releases that completed binding for a later explicit submission. Unknown,
cancelled and disconnected operations keep their original binding.

Direct business Runtime replies must match both the captured scope and operation
ID, including failure replies. A mismatch stays unconfirmed without accepting
the returned envelope. Result-query outer replies keep their existing protocol
handling; cached original-response validation remains unchanged.

The CLI dependency is PR #317 head `79d2777`, merged as `d926f09`.
Its committed key replies retain the original operation ID, source profile and
committed outcome alongside the existing success fields. CLI DEV12 release run
[43857691588](https://github.com/risense-ai/respire-cli/actions/runs/43857691588)
is in progress at this documentation update. This Client2 PR does not modify CLI
or Runtime and does not claim final published-stack acceptance has passed.

## Validation and release sequence

Use the existing Go tests and vet with the `production` tag, frontend syntax
checks, `scripts/test-account-session-state.cjs` and
`scripts/test-client-refresh.cjs`. Do not add repository tests for this task.
PR CI runs the existing light checks; no tag or native release is requested.

The latest explicit user authorization supersedes the former manual
pre-release native acceptance gate: publish after source CI passes, then run the
full native integration matrix once, in parallel, after the final CLI, Runtime
and Client2 Releases are public. Do not run the same full matrix before release.
Freeze those final public assets and their hashes for that acceptance run.
Cover host routing, engine child identities, failure/pending recovery, restart
scope fencing and issued recovery-material acknowledgement. Use synthetic
accounts and a dedicated data root and credential namespace. Full native
integration acceptance remains unverified until that post-release run completes;
source CI alone does not prove cloud login, native keyring, login autostart or
end-to-end compatibility.

Local Windows amd64 validation passed: existing `go test -tags production
./...`, `go vet -tags production ./...`, all 33 frontend JavaScript syntax
checks, the existing 34 account/session and 12 refresh checks, PowerShell build
script parsing, and a production desktop build reporting `2.0.0-dev.13`.
An ignored assertion program also exercised failed original receipts,
scope preservation across a desktop restart, pending host identity and
secret-free journals, separate model child identities, and an unknown model
probe without a recovery write. These used synthetic loopback and CLI protocol
fixtures, not a real account, credential store, database or inference engine.

The continuation increment also passed isolated assertions using a synthetic CLI
executable and loopback replies: account/key query after restart without live
Runtime health, original host identity with separate transport IDs, capability
rejection, committed/awaiting-verification queries without replay, mismatched
continuation receipts staying unknown, private/admission inputs rejected, and
secret-free journals. Direct successful and failed business replies with wrong
scope/opid were rejected; result-query outer replies were handled separately.
An isolated browser using the production authentication module exercised cancel,
TOTP, explicit no-merge (`false`), two independent transport IDs under the same
job, actual committed-state handling and mandatory recovery-material save
acknowledgement. These are fixture checks, not native account acceptance.
Follow-up isolated checks verified browser admission through the stdio method,
structured pending followed by original-account continuation, and failed-query
cache release for an explicit corrected submission. Unknown/disconnected queries
remained bound without replay. These do not prove a real OAuth authorization.
The existing account test harness only gained bindings for the new shared auth
helper; no repository test cases were added.

Runtime DEV23 publication and the next CLI pin/release are managed upstream.
The first DEV22 build was cancelled for a macOS launch-claim test fixture path
canonicalization fix. Runtime DEV23 is planned to fix the subsequent DEV22 timeout.
CLI targets DEV12 independently of Client2 DEV13. DEV23 is the required upgrade
threshold; protocol compatibility does not require matching component or Core
SDK versions. Merge the host baseline changes only after upstream verifies the
published Runtime DEV23 release, then release the hosts. This preparation does
not claim a published new-stack acceptance result.
