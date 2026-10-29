# Required CLI authentication transport

The legacy desktop passes passwords/recovery values through CLI process arguments. The current CLI exposes registration, password login, key recovery and super-password reset; only classification keys currently support redirected standard input. Reproducing those process arguments would expose credentials through process inspection.

## Implemented transport and compatibility

CLI PR [#170](https://github.com/risense-ai/respire-cli/pull/170) implements host-only `--host-rpc-stdio`. It reads a bounded JSON request from stdin, validates fixed methods and credential fields, then dispatches existing host authentication/profile transactions. Ordinary HTTP `cli.exec` retains its host-only gate: an account commit can restart that memory Runtime, so the host transport owns the request and final receipt.

Use a published CLI package advertising host RPC version 1. The compatible release line starts with `1.0.18-dev.1`; verify availability in the official CLI release list rather than inferring it from the client version. Stable/Dev selection does not silently change to enable an account operation.

Methods are `auth.register`, `auth.login` (including TOTP), `auth.recover` and `auth.reset`. The request is `{v:1,id,method,params}`; a reply contains `{v:1,id,ok,exit,envelope}`. Input is limited to 64 KiB and fields to 16 KiB. Unknown fields, malformed/trailing JSON and client-only invocation are rejected. CLI capabilities advertise transport version 1. The client reports an explicit upgrade error for older packages and never automatically replays authentication after an unknown outcome.

The client will pass only the host-entry flag on the command line. Credentials remain in stdin/process memory; they must not enter desktop preferences, request journals, logs or release assets. Returned recovery material is displayed explicitly for the user to save. Authentication failures must not echo submitted credentials.

## Acceptance before client release

- Validated parsing and rejection using existing CLI checks and an isolated host/fixture service.
- Verify the actual child command line contains no password, recovery code or key material.
- Verify readonly/off mode and failed authentication preserve the prior profile.
- Verify registration/login/recovery/reset and the user acknowledgement of issued recovery material.
- Publish a compatible CLI Dev package, verify its artifact identity, then exercise the new desktop flows with that package.

The implementation is in an isolated CLI worktree; the original CLI worktree's unrelated changes were retained. Existing auth, login, capability/revision and profile-transaction checks passed locally. An isolated TOTP failure probe confirmed credential-free process arguments. Successful cloud registration/recovery and native credential-store acceptance remain release gates; local unit checks do not prove these integrations.
