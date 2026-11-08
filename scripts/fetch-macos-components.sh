#!/usr/bin/env bash
# Fetch the macOS bootstrap components (managed Runtime + CLI) for the DMG.
#
# One channel is used for both components. Selection, metadata normalization,
# identity checks and the CLI/Runtime compat window are enforced by the Go
# helper cmd/bootstrap-metadata, which reuses internal/host so the build-time
# rules cannot drift from the client's online installer. This script then
# downloads the three artifacts, verifies every SHA-256, expands the Runtime
# archive, verifies the full core-runtime.json tree, and assembles the
# read-only payload directory the client discovers on first launch:
#
#   <out>/bootstrap.json     normalized embedded-install manifest
#   <out>/rsrs               CLI binary
#   <out>/runtime/           expanded Runtime tree (rsrs-runtime + core files)
#
# Set FETCH_FIXTURES_DIR to a directory containing runtime-releases.json and
# cli-releases.json (with browser_download_url values pointing at local files)
# to run fully offline against fixtures; this is how the self-test exercises
# the failure modes without network access.
#
# Usage: fetch-macos-components.sh --channel <stable|dev> --target <triple> --out <dir> [--cache <dir>]
set -euo pipefail
cd "$(dirname "$0")/.."

channel=stable
target=""
out=""
cache=".tools/components-cache"
while [[ $# -gt 0 ]]; do
    case "$1" in
        --channel) channel="$2"; shift 2 ;;
        --target) target="$2"; shift 2 ;;
        --out) out="$2"; shift 2 ;;
        --cache) cache="$2"; shift 2 ;;
        *) echo "unknown flag: $1" >&2; exit 1 ;;
    esac
done
[[ "$channel" == stable || "$channel" == dev ]] || { echo "unsupported channel: $channel" >&2; exit 1; }
[[ -n "$target" && -n "$out" ]] || { echo "--target and --out are required" >&2; exit 1; }
[[ "$target" == aarch64-apple-darwin ]] || { echo "the full DMG payload only targets aarch64-apple-darwin, got $target" >&2; exit 1; }
[[ -n "$cache" && "$cache" != / && "$cache" != "$HOME" ]] || { echo "unsafe cache dir" >&2; exit 1; }

if [[ -n "${FETCH_FIXTURES_DIR:-}" ]]; then
    runtime_list="$FETCH_FIXTURES_DIR/runtime-releases.json"
    cli_list="$FETCH_FIXTURES_DIR/cli-releases.json"
    [[ -f "$runtime_list" && -f "$cli_list" ]] || { echo "fixtures need runtime-releases.json and cli-releases.json" >&2; exit 1; }
else
    runtime_list="https://api.github.com/repos/risense-ai/respire-runtime/releases?per_page=20"
    cli_list="https://api.github.com/repos/risense-ai/respire-cli/releases?per_page=20"
fi

mkdir -p "$out" "$cache"

# resolve: run the metadata helper and split its fetch plan into shell vars.
# Aborts on any selection/identity/compat failure with the helper's message.
resolve() {
    local extra=()
    [[ -n "${FETCH_FIXTURES_DIR:-}" ]] && extra+=(--fixtures)
    go run ./cmd/bootstrap-metadata "${extra[@]+${extra[@]}}" \
        --channel "$channel" --target "$target" \
        --runtime-releases "$runtime_list" --cli-releases "$cli_list"
}

fetch_artifact() { # <url-or-path> <dest>
    case "$1" in
        https://*|http://*) curl -fsSL --retry 2 -o "$2" "$1" ;;
        *) cp "$1" "$2" ;;
    esac
}

sha_of() { shasum -a 256 "$1" | awk '{print $1}'; }

# verify_tree <runtime-dir> <expected-binary-sha256> <target>: full-file check
# mirroring the client's verifyRuntime so a truncated expansion cannot enter
# the payload.
verify_tree() {
    python3 - "$1" "$2" "$3" <<'PY'
import hashlib, json, os, sys
root, binary_sha, target = sys.argv[1], sys.argv[2], sys.argv[3]
with open(os.path.join(root, "core-runtime.json"), "rb") as f:
    manifest = json.load(f)
if manifest.get("schema_version") != 1 or manifest.get("target") != target:
    sys.exit("core-runtime.json schema/target mismatch")
if not manifest.get("files"):
    sys.exit("core-runtime.json lists no files")
for entry in manifest["files"]:
    path = entry["path"]
    if path.startswith("/") or "\\" in path or ".." in path.split("/"):
        sys.exit(f"unsafe manifest path: {path}")
    digest = hashlib.sha256(open(os.path.join(root, path), "rb").read()).hexdigest()
    if digest != entry["sha256"]:
        sys.exit(f"hash mismatch: {path}")
digest = hashlib.sha256(open(os.path.join(root, "rsrs-runtime"), "rb").read()).hexdigest()
if digest != binary_sha:
    sys.exit("runtime binary hash mismatch")
PY
}

# assemble <staging-runtime-dir> <cli> <cli-version> <cli-sha> <rt-version> <rt-sha> <min-cli> <max-cli>:
# copy the verified tree into the payload layout and write bootstrap.json.
assemble() {
    local rt_dir="$1" cli="$2" cli_version="$3" cli_sha="$4" version="$5" binary_sha="$6" min_cli="$7" max_cli="$8"
    rm -rf "$out/runtime"
    cp -R "$rt_dir" "$out/runtime"
    cp "$cli" "$out/rsrs"
    chmod 0755 "$out/rsrs" "$out/runtime/rsrs-runtime"
    python3 - "$out" "$target" "$channel" "$cli_version" "$cli_sha" "$version" "$binary_sha" "$min_cli" "$max_cli" <<'PY'
import json, os, sys
out, target, channel, cli_version, cli_sha, version, binary_sha, min_cli, max_cli = sys.argv[1:10]
manifest = {
    "schema_version": 1,
    "target": target,
    "channel": channel,
    "cli": {"version": cli_version, "sha256": cli_sha},
    "runtime": {"version": version, "sha256": binary_sha, "min_cli": min_cli, "max_cli": max_cli},
}
with open(os.path.join(out, "bootstrap.json"), "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
PY
    # The payload must contain nothing but regular files and directories.
    if find "$out" -mindepth 1 ! -type f ! -type d | grep -q .; then
        echo "payload contains non-regular entries" >&2
        exit 1
    fi
    [[ -x "$out/rsrs" && -x "$out/runtime/rsrs-runtime" ]] || { echo "payload binaries lost their executable bit" >&2; exit 1; }
    verify_tree "$out/runtime" "$binary_sha" "$target"
}

plan=$(resolve)
cache_key=$(printf '%s' "$plan" | python3 -c "import json,sys; p=json.load(sys.stdin); print('rt-{}-{}-cli-{}-{}'.format(p['runtime']['version'], p['runtime']['archive_sha256'][:12], p['cli']['version'], p['cli']['sha256'][:12]))")
work="$cache/$cache_key"

# Flatten the plan into PLAN_* variables (URLs, hashes, versions).
while IFS= read -r line; do
    key=${line%%=*}
    printf -v "PLAN_$key" '%s' "${line#*=}"
done < <(printf '%s' "$plan" | python3 -c 'import json,sys; p=json.load(sys.stdin)
print("cli_version=" + p["cli"]["version"])
print("cli_sha=" + p["cli"]["sha256"])
print("cli_url=" + p["cli"]["binary_url"])
print("rt_version=" + p["runtime"]["version"])
print("rt_sha=" + p["runtime"]["sha256"])
print("rt_url=" + p["runtime"]["binary_url"])
print("rt_archive=" + p["runtime"]["archive_file"])
print("rt_archive_sha=" + p["runtime"]["archive_sha256"])
print("rt_archive_url=" + p["runtime"]["archive_url"])
print("rt_min_cli=" + p["runtime"]["min_cli"])
print("rt_max_cli=" + p["runtime"]["max_cli"])')

if [[ -f "$work/.complete" ]]; then
    echo "cache hit: $work" >&2
else
    rm -rf "$work"
    mkdir -p "$work/runtime"
    fetch_artifact "$PLAN_rt_url" "$work/rsrs-runtime-download"
    fetch_artifact "$PLAN_rt_archive_url" "$work/runtime.tar.gz"
    fetch_artifact "$PLAN_cli_url" "$work/rsrs-cli"
    [[ "$(sha_of "$work/rsrs-runtime-download")" == "$PLAN_rt_sha" ]] || { echo "Runtime binary SHA256 mismatch" >&2; exit 1; }
    [[ "$(sha_of "$work/runtime.tar.gz")" == "$PLAN_rt_archive_sha" ]] || { echo "Runtime archive SHA256 mismatch" >&2; exit 1; }
    [[ "$(sha_of "$work/rsrs-cli")" == "$PLAN_cli_sha" ]] || { echo "CLI binary SHA256 mismatch" >&2; exit 1; }
    tar -xzf "$work/runtime.tar.gz" -C "$work/runtime"
    if find "$work/runtime" ! -type f ! -type d | grep -q .; then
        echo "Runtime archive contains non-regular entries" >&2
        exit 1
    fi
    cp "$work/rsrs-runtime-download" "$work/runtime/rsrs-runtime"
    chmod 0755 "$work/runtime/rsrs-runtime"
    verify_tree "$work/runtime" "$PLAN_rt_sha" "$target"
    cp "$work/rsrs-cli" "$work/rsrs"
    chmod 0755 "$work/rsrs"
    printf '%s\n' "$plan" > "$work/fetch.json"
    : > "$work/.complete"
fi

assemble "$work/runtime" "$work/rsrs" "$PLAN_cli_version" "$PLAN_cli_sha" "$PLAN_rt_version" "$PLAN_rt_sha" "$PLAN_rt_min_cli" "$PLAN_rt_max_cli"
echo "payload ready: $out (Runtime $PLAN_rt_version, CLI $PLAN_cli_version, channel $channel)" >&2
