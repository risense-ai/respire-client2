#!/usr/bin/env bash
# Self-test for scripts/fetch-macos-components.sh against local release
# fixtures (no network): the success path plus the failure modes the change
# requires — tampered hashes, a missing target asset, an out-of-range CLI
# compat window, and a stable channel with no full release. Also proves the
# version+hash cache reuse path by removing the fixture payloads between runs.
set -euo pipefail
cd "$(dirname "$0")/.."

target=aarch64-apple-darwin
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# fixture <dir> <cli-version> <compat-max-cli> <with-runtime-build-asset> [archive-sha-override]
# Writes a complete, internally consistent fixture set; callers then tamper
# with individual pieces for the negative cases.
fixture() {
    local dir="$1" cli_version="$2" max_cli="$3" with_build="$4"
    mkdir -p "$dir/payload/core-notices"
    printf 'fake-runtime-binary\n' > "$dir/payload/rsrs-runtime-$target"
    printf 'Respire Runtime notices\n' > "$dir/payload/core-notices/NOTICES.txt"
    chmod 0755 "$dir/payload/rsrs-runtime-$target"
    local bin_sha notice_sha
    bin_sha=$(shasum -a 256 "$dir/payload/rsrs-runtime-$target" | awk '{print $1}')
    notice_sha=$(shasum -a 256 "$dir/payload/core-notices/NOTICES.txt" | awk '{print $1}')
    python3 - "$dir/payload/core-runtime.json" "$target" "$bin_sha" "$notice_sha" <<'PY'
import json, sys
path, target, bin_sha, notice_sha = sys.argv[1:5]
manifest = {"schema_version": 1, "target": target, "files": [
    {"path": f"rsrs-runtime-{target}", "sha256": bin_sha},
    {"path": "core-notices/NOTICES.txt", "sha256": notice_sha},
]}
with open(path, "w") as f:
    json.dump(manifest, f, indent=2)
    f.write("\n")
PY
    (cd "$dir/payload" && tar -czf "$dir/rsrs-runtime-$target.tar.gz" .)
    printf 'fake-cli-binary %s\n' "$cli_version" > "$dir/rsrs-$target"
    printf 'fake-runtime-binary\n' > "$dir/rsrs-runtime-$target"

    python3 - "$dir" "$cli_version" "$max_cli" "$with_build" "$target" <<'PY'
import hashlib, json, os, sys
d, cli_version, max_cli, with_build, target = sys.argv[1:6]
sha = lambda p: hashlib.sha256(open(p, "rb").read()).hexdigest()
runtime_build = {
    "schema_version": 2,
    "runtime_version": "1.0.21-dev.2",
    "tag": "v1.0.21-dev.2",
    "target_triple": target,
    "archive_file": f"rsrs-runtime-{target}.tar.gz",
    "archive_sha256": sha(os.path.join(d, f"rsrs-runtime-{target}.tar.gz")),
    "binary_file": f"rsrs-runtime-{target}",
    "binary_sha256": sha(os.path.join(d, f"rsrs-runtime-{target}")),
    "compat": {"min_cli": "0.4.0", "max_cli": max_cli},
}
cli_build = {
    "schema_version": 1,
    "version": cli_version,
    "target": target,
    "git_sha": "dd45330c9848ebb73f2d98a363e6af0de3fc5a91",
    "binary_file": f"rsrs-{target}",
    "runtime_file": f"rsrs-{target}-runtime.tar.gz",
    "binary_sha256": sha(os.path.join(d, f"rsrs-{target}")),
    "runtime_sha256": "0" * 64,
}
dl = lambda repo, tag, name: f"https://github.com/risense-ai/{repo}/releases/download/{tag}/{name}"
runtime_assets = [{"name": f"rsrs-runtime-{target}", "browser_download_url": os.path.join(d, f"rsrs-runtime-{target}")},
                  {"name": f"rsrs-runtime-{target}.tar.gz", "browser_download_url": os.path.join(d, f"rsrs-runtime-{target}.tar.gz")}]
if with_build == "yes":
    runtime_assets.append({"name": f"runtime-build-{target}.json", "browser_download_url": os.path.join(d, f"runtime-build-{target}.json")})
    with open(os.path.join(d, f"runtime-build-{target}.json"), "w") as f:
        json.dump(runtime_build, f, indent=2)
        f.write("\n")
runtime_releases = [{"tag_name": "v1.0.21-dev.2", "draft": False, "prerelease": True, "assets": runtime_assets},
                    {"tag_name": "v1.0.20", "draft": False, "prerelease": False,
                     "assets": [{"name": f"rsrs-runtime-{target}", "browser_download_url": dl("respire-runtime", "v1.0.20", f"rsrs-runtime-{target}")}]}]
with open(os.path.join(d, "runtime-releases.json"), "w") as f:
    json.dump(runtime_releases, f, indent=2)
cli_releases = [{"tag_name": f"v{cli_version}", "draft": False, "prerelease": "-" in cli_version,
                 "assets": [{"name": f"cli-build-{target}.json", "browser_download_url": os.path.join(d, f"cli-build-{target}.json")},
                            {"name": f"rsrs-{target}", "browser_download_url": os.path.join(d, f"rsrs-{target}")}]}]
with open(os.path.join(d, f"cli-build-{target}.json"), "w") as f:
    json.dump(cli_build, f, indent=2)
    f.write("\n")
with open(os.path.join(d, "cli-releases.json"), "w") as f:
    json.dump(cli_releases, f, indent=2)
PY
}

run_fetch() { # <fixtures> <out> <cache> <channel> [expected-failure-pattern]
    local fixtures="$1" out="$2" cache="$3" channel="$4" pattern="${5:-}"
    local status=0
    FETCH_FIXTURES_DIR="$fixtures" bash scripts/fetch-macos-components.sh \
        --channel "$channel" --target "$target" --out "$out" --cache "$cache" 2>"$tmp/last-stderr" || status=$?
    if [[ -n "$pattern" ]]; then
        [[ $status -ne 0 ]] || fail "expected failure ($pattern), command succeeded"
        grep -q "$pattern" "$tmp/last-stderr" || fail "stderr missing '$pattern': $(cat "$tmp/last-stderr")"
    else
        [[ $status -eq 0 ]] || fail "expected success, got: $(cat "$tmp/last-stderr")"
    fi
}

# 1. Success path (dev channel, prerelease runtime + prerelease CLI win).
fixture "$tmp/ok" "0.4.1-dev.1" "*" yes
run_fetch "$tmp/ok" "$tmp/out-ok" "$tmp/cache-ok" dev
[[ -x "$tmp/out-ok/rsrs" ]] || fail "payload CLI missing or not executable"
[[ -x "$tmp/out-ok/runtime/rsrs-runtime-$target" && -x "$tmp/out-ok/runtime/rsrs-runtime" ]] || fail "payload runtime binaries wrong"
[[ -f "$tmp/out-ok/runtime/core-runtime.json" ]] || fail "core-runtime.json missing from payload"
[[ ! -f "$tmp/out-ok/fetch.json" ]] || fail "fetch.json must not ship in the payload"
python3 - "$tmp/out-ok/bootstrap.json" <<'PY'
import json, sys
m = json.load(open(sys.argv[1]))
assert m["schema_version"] == 1, m
assert m["target"] == "aarch64-apple-darwin", m
assert m["channel"] == "dev", m
assert m["cli"]["version"] == "0.4.1-dev.1", m
assert m["runtime"]["version"] == "1.0.21-dev.2", m
assert len(m["runtime"]["sha256"]) == 64 and len(m["cli"]["sha256"]) == 64, m
assert m["runtime"]["min_cli"] == "0.4.0" and m["runtime"]["max_cli"] == "*", m
PY
echo "ok: success path"

# 2. Cache reuse: destroy the fixture payloads, rerun with the same cache and
# expect success with byte-identical artifacts.
cli_sha_before=$(shasum -a 256 "$tmp/out-ok/rsrs" | awk '{print $1}')
rm -f "$tmp/ok"/rsrs-runtime-* "$tmp/ok/rsrs-$target" "$tmp/ok"/*.tar.gz
run_fetch "$tmp/ok" "$tmp/out-ok" "$tmp/cache-ok" dev
grep -q "cache hit" "$tmp/last-stderr" || fail "second run must hit the cache"
[[ "$(shasum -a 256 "$tmp/out-ok/rsrs" | awk '{print $1}')" == "$cli_sha_before" ]] || fail "cached CLI bytes changed"
echo "ok: cache reuse"

# 3. Tampered CLI payload bytes (fresh cache, metadata hash unchanged).
fixture "$tmp/tamper-cli" "0.4.1-dev.1" "*" yes
printf 'tampered\n' > "$tmp/tamper-cli/rsrs-$target"
run_fetch "$tmp/tamper-cli" "$tmp/out-tamper-cli" "$tmp/cache-tamper-cli" dev "CLI binary SHA256 mismatch"
echo "ok: CLI hash tamper rejected"

# 4. Tampered Runtime archive bytes (fresh cache).
fixture "$tmp/tamper-arch" "0.4.1-dev.1" "*" yes
printf 'x' >> "$tmp/tamper-arch/rsrs-runtime-$target.tar.gz"
run_fetch "$tmp/tamper-arch" "$tmp/out-tamper-arch" "$tmp/cache-tamper-arch" dev "Runtime archive SHA256 mismatch"
echo "ok: archive hash tamper rejected"

# 5. Target asset missing: the runtime-build metadata asset is absent.
fixture "$tmp/missing-target" "0.4.1-dev.1" "*" no
run_fetch "$tmp/missing-target" "$tmp/out-missing" "$tmp/cache-missing" dev "does not carry runtime-build"
echo "ok: missing target asset rejected"

# 6. CLI outside the declared compat window.
fixture "$tmp/compat" "0.4.1-dev.1" "0.4.0" yes
run_fetch "$tmp/compat" "$tmp/out-compat" "$tmp/cache-compat" dev "compat"
echo "ok: compat violation rejected"

# 7. Stable channel with no full release must fail loudly.
fixture "$tmp/stable" "0.4.1" "*" yes
python3 - "$tmp/stable/runtime-releases.json" <<'PY'
import json, sys
path = sys.argv[1]
# The runtime train has no full release yet: only prereleases exist.
releases = [r for r in json.load(open(path)) if r["prerelease"]]
json.dump(releases, open(path, "w"), indent=2)
PY
run_fetch "$tmp/stable" "$tmp/out-stable" "$tmp/cache-stable" stable "no published Runtime release"
echo "ok: stable without full release rejected"

echo "all fetch-macos-components fixture cases passed"
