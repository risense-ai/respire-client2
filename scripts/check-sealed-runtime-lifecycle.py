"""Exercise sealed Client2 without an account or CLI on a fresh hosted runner."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tarfile
import tempfile
import urllib.request


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def check(report):
    require(os.environ.get("GITHUB_ACTIONS") == "true"
            and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted",
            "Use a fresh GitHub-hosted runner")
    require(shutil.which("rsrs") is None, "Runner already has a CLI on PATH")
    platform = os.environ["TARGET_PLATFORM"]
    target = os.environ["RUNTIME_TARGET"]
    version = os.environ["RUNTIME_VERSION"]
    require(re.fullmatch(r"\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", version), "Invalid Runtime version")
    repo = "risense-ai/respire-runtime"
    release = json.loads(subprocess.check_output(["gh", "api", f"repos/{repo}/releases/tags/v{version}"]))
    require(release["tag_name"] == f"v{version}" and not release["draft"], "Runtime release differs")
    metadata_name = f"runtime-build-{target}.json"
    asset, = [item for item in release["assets"] if item["name"] == metadata_name]
    raw_meta = subprocess.check_output(
        ["gh", "api", "-H", "Accept: application/octet-stream", f"repos/{repo}/releases/assets/{asset['id']}"]
    )
    require(asset["digest"] == "sha256:" + hashlib.sha256(raw_meta).hexdigest(), "Runtime metadata digest differs")
    metadata = json.loads(raw_meta)
    require(metadata["runtime_version"] == version and metadata["target_triple"] == target,
            "Runtime metadata version or platform differs")
    archive, = [item for item in release["assets"] if item["name"] == metadata["archive_file"]]
    require(archive["digest"] == "sha256:" + metadata["archive_sha256"], "Runtime archive digest differs")
    report["runtime"] = {
        "version": version, "target": target, "archive_id": archive["id"],
        "archive_sha256": metadata["archive_sha256"], "image_sha256": metadata["binary_sha256"],
    }
    checks = report["checks"]
    with tempfile.TemporaryDirectory(dir=os.environ["RUNNER_TEMP"]) as temporary:
        root = Path(temporary).resolve()
        sealed = Path("sealed").resolve()
        manifest = json.loads((sealed / "candidate.json").read_text(encoding="utf-8"))
        package = sealed / manifest["targets"][platform][0]
        require(digest(package) == manifest["files"][package.name]["sha256"], "Client2 package digest differs")
        report["client2"] = {
            "version": manifest["version"], "package": package.name, "sha256": digest(package),
            "producer": manifest["producer"],
        }
        if platform.startswith("windows-"):
            image = package
        elif platform.startswith("darwin-"):
            subprocess.run(["ditto", "-x", "-k", package, root], check=True)
            image = root / "Respire.app/Contents/MacOS/respire"
        else:
            with tarfile.open(package) as bundle:
                bundle.extractall(root, filter="data")
            image = root / "respire"

        profile = root / "profile"
        (profile / "client2").mkdir(parents=True)
        (profile / "client2/preferences.json").write_text('{"runtimeAutoRestart":false}', encoding="utf-8")
        with socket.socket() as reserved:
            reserved.bind(("127.0.0.1", 0))
            port = reserved.getsockname()[1]
        child_env = dict(os.environ, PATH="", RSRS_DATA_DIR=str(profile), RSRS_RPC_PORT=str(port))
        child_env.pop("GH_TOKEN", None)
        require("RSRS_CORE_TEST_MODE" not in child_env, "Core test mode is forbidden")

        def client(action, data_dir=profile, success=True):
            process = subprocess.run([image, "--headless", action], cwd=root, capture_output=True,
                                     env=dict(child_env, RSRS_DATA_DIR=str(data_dir)), timeout=180)
            result = json.loads(process.stdout)
            require(result["ok"] is success and (process.returncode == 0) is success,
                    f"Unexpected Client2 result for {action}: {result.get('error')}")
            return result.get("result")

        def assert_runtime_only():
            state = client("bootstrap-status")
            require(not any(state.get(key) for key in
                            ("cli_path", "cli_error", "payload_found", "payload_error", "installed_runtime_error")),
                    "Unexpected CLI, bootstrap payload, or installed Runtime error")
            return state.get("installed_runtime")

        release_info = subprocess.check_output([image, "--version"], env=child_env, cwd=root, timeout=30)
        require(json.loads(release_info) == {"version": manifest["version"], "platform": platform},
                "Client2 executable identity differs")
        require(not assert_runtime_only(), "Fixture already has a Runtime selection")
        checks.append("fresh_profile_no_cli")
        client("install-runtime")
        expected = {"version": version, "sha256": metadata["binary_sha256"]}
        require(assert_runtime_only() == expected, "Installed selection differs from verified release")
        runtime_dir = profile / "bin/runtime" / version
        for record in metadata["files"]:
            filename = record["path"]
            if filename.startswith("core-notices/"):
                continue
            if filename == metadata["binary_file"]:
                filename = "rsrs-runtime.exe" if platform.startswith("windows-") else "rsrs-runtime"
            installed = runtime_dir / filename
            require(installed.stat().st_size == record["size"] and digest(installed) == record["sha256"],
                    f"Installed Runtime file differs: {filename}")
        checks.append("online_install_verified")

        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        health_url = f"http://127.0.0.1:{port}/api/health"

        def health():
            request = urllib.request.Request(health_url, headers={"X-Respire-Health": "runtime-v1"})
            with opener.open(request, timeout=5) as response:
                return json.load(response)

        client("status", success=False)
        client("start")
        try:
            initial = health()
            require(initial["server"] == "respire" and initial["v"] == 1 and initial["startup_complete"]
                    and initial["runtime_version"] == version and initial["image_sha256"] == expected["sha256"]
                    and Path(initial["installation_root"]).resolve() == profile.resolve()
                    and initial["credential_state"] == "required" and initial["store_ready"] is False,
                    "Runtime did not report the verified accountless locked instance")
            checks.append("started_verified_locked")
            identity = {key: initial[key] for key in ("pid", "process_start_id", "instance_id", "generation")}
            client("start")
            repeated = health()
            require(all(repeated[key] == value for key, value in identity.items()), "Repeated start replaced the instance")
            checks.append("repeat_start_same_instance")
            foreign = root / "foreign"
            foreign.mkdir()
            client("stop", data_dir=foreign, success=False)
            repeated = health()
            require(all(repeated[key] == value for key, value in identity.items()), "Foreign stop changed the instance")
            checks.append("foreign_stop_rejected")
        finally:
            client("stop")
        client("status", success=False)
        receipt = profile / "runtime" / f"stop-{initial['instance_id']}.json"
        require(json.loads(receipt.read_bytes())["state"] == "drained", "Missing drained stop receipt")
        require(assert_runtime_only() == expected, "Runtime selection or CLI absence changed")
        checks.append("owned_stop_drained_no_cli")


if __name__ == "__main__":
    result = {"status": "failed", "scope": "sealed package, accountless native lifecycle", "checks": []}
    try:
        check(result)
        result["status"] = "passed"
    finally:
        Path("lifecycle-report.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
