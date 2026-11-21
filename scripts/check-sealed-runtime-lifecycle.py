"""Exercise Client2 without CLI on a fresh hosted runner."""

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
import time
import urllib.request
import uuid


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
    install_exact = os.environ.get("INSTALL_EXACT_RUNTIME") == "true"
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
    if "RUNTIME_ARCHIVE_SHA256" in os.environ:
        require(metadata["archive_sha256"] == os.environ["RUNTIME_ARCHIVE_SHA256"],
                "Runtime archive differs from the pinned test fixture")
    report["runtime"] = {
        "version": version, "target": target, "archive_id": archive["id"],
        "archive_sha256": metadata["archive_sha256"], "image_sha256": metadata["binary_sha256"],
    }
    checks = report["checks"]
    with tempfile.TemporaryDirectory(dir=os.environ["RUNNER_TEMP"]) as temporary:
        root = Path(temporary).resolve()
        if "CLIENT2_PR_PACKAGE" in os.environ:
            require(os.environ["GITHUB_EVENT_NAME"] == "pull_request" and platform.startswith("linux-"),
                    "Use the unpublished package path only in the native Linux PR check")
            package = Path(os.environ["CLIENT2_PR_PACKAGE"]).resolve()
            candidate_version = os.environ["CANDIDATE_VERSION"]
            producer = {"source": os.environ["GITHUB_SHA"], "run_id": os.environ["GITHUB_RUN_ID"]}
        else:
            sealed = Path("sealed").resolve()
            manifest = json.loads((sealed / "candidate.json").read_text(encoding="utf-8"))
            package = sealed / manifest["targets"][platform][0]
            require(digest(package) == manifest["files"][package.name]["sha256"], "Client2 package digest differs")
            candidate_version, producer = manifest["version"], manifest["producer"]
        report["client2"] = {
            "version": candidate_version, "package": package.name, "sha256": digest(package),
            "producer": producer,
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
        require("RSRS_CORE_TEST_MODE" not in os.environ, "Core test mode is forbidden")
        # Runtime uses the native ps on macOS to bind its process birth.
        fixture_path = "" if platform.startswith("windows-") else "/usr/bin:/bin"
        require(shutil.which("rsrs", path=fixture_path) is None, "CLI is reachable from the fixture PATH")
        child_env = {
            key: value for key, value in os.environ.items()
            if key.upper() in {"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "SYSTEMROOT", "WINDIR",
                               "COMSPEC", "PATHEXT", "PROGRAMFILES", "PROGRAMDATA", "TEMP", "TMP", "TMPDIR",
                               "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR",
                               "LANG", "LC_ALL", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}
        }
        child_env.update(PATH=fixture_path, RSRS_DATA_DIR=str(profile), RSRS_RPC_PORT=str(port))

        def client(action, *args, data_dir=profile, expected_error=None):
            process = subprocess.run([image, "--headless", action, *args], cwd=root, capture_output=True,
                                     env=dict(child_env, RSRS_DATA_DIR=str(data_dir)), timeout=180)
            result = json.loads(process.stdout)
            success = expected_error is None
            require(result["ok"] is success and process.returncode == (0 if success else 1)
                    and result.get("error") == expected_error,
                    f"Unexpected Client2 result for {action}: {result.get('error')}")
            return result.get("result")

        def assert_runtime_only():
            state = client("bootstrap-status")
            require(not any(state.get(key) for key in
                            ("cli_path", "cli_error", "payload_found", "payload_error", "installed_runtime_error")),
                    "Unexpected CLI, bootstrap payload, or installed Runtime error")
            return state.get("installed_runtime")

        release_info = subprocess.check_output([image, "--version"], env=child_env, cwd=root, timeout=30)
        require(json.loads(release_info) == {"version": candidate_version, "platform": platform},
                "Client2 executable identity differs")
        require(not assert_runtime_only(), "Fixture already has a Runtime selection")
        checks.append("fresh_profile_no_cli")
        if install_exact:
            client("install-runtime", version)
        else:
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
        checks.append("exact_online_install_verified" if install_exact else "online_install_verified")

        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        health_url = f"http://127.0.0.1:{port}/api/health"

        def health():
            request = urllib.request.Request(health_url, headers={"X-Respire-Health": "runtime-v1"})
            with opener.open(request, timeout=5) as response:
                return json.load(response)

        def assert_listener_absent():
            try:
                with socket.create_connection(("127.0.0.1", port), timeout=5):
                    raise RuntimeError("The fixture port still has a listener")
            except ConnectionRefusedError:
                return

        assert_listener_absent()
        try:
            client("start")
        except RuntimeError:
            logs = list((profile / "client2").glob("runtime-start-*.log"))
            if len(logs) == 1:
                with logs[0].open("rb") as stream:
                    report["accountless_startup_log"] = stream.read(4096).decode("utf-8", errors="replace")
            raise
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
            client("stop", data_dir=foreign, expected_error="独立 Runtime 未报告完整的停止身份")
            repeated = health()
            require(all(repeated[key] == value for key, value in identity.items()), "Foreign stop changed the instance")
            checks.append("foreign_stop_rejected")
        finally:
            client("stop")
        assert_listener_absent()
        receipt = profile / "runtime" / f"stop-{initial['instance_id']}.json"
        require(json.loads(receipt.read_bytes())["state"] == "drained", "Missing drained stop receipt")
        require(assert_runtime_only() == expected, "Runtime selection or CLI absence changed")
        checks.append("owned_stop_drained_no_cli")

        if "SEED_CLI" in os.environ:
            require(platform.startswith("linux-") and install_exact, "Linux seed requires exact Runtime selection")
            seed = Path(os.environ["SEED_CLI"]).resolve()
            require(seed.parent == Path(os.environ["RUNNER_TEMP"]).resolve()
                    and digest(seed) == os.environ["SEED_CLI_SHA256"], "Incorrect isolated CLI seed")
            report["seed_cli"] = {"name": seed.name, "sha256": digest(seed)}
            check_linux_key_handoff(root, child_env, seed, client, health, assert_runtime_only, opener, port, report)


def check_linux_key_handoff(root, env, seed, client, health, assert_runtime_only, opener, port, report):
    checks = report["checks"]
    require(env.get("DBUS_SESSION_BUS_ADDRESS", "").startswith("unix:"), "Private session bus required")
    private = root / "credentials"
    control = private / "run"
    control.mkdir(mode=0o700, parents=True)
    env.update(HOME=str(private), XDG_DATA_HOME=str(private / "data"), XDG_CONFIG_HOME=str(private / "config"),
               XDG_CACHE_HOME=str(private / "cache"), XDG_RUNTIME_DIR=str(control))
    keyring_env = dict(env, GNOME_KEYRING_CONTROL=str(control))

    def dbus(destination, path, method, *args):
        return subprocess.check_output(["gdbus", "call", "--session", "--dest", destination,
                                        "--object-path", path, "--method", method, *args],
                                       env=keyring_env, text=True, timeout=5).strip()

    bus = "org.freedesktop.DBus"
    service = "org.freedesktop.secrets"
    bus_path = "/org/freedesktop/DBus"
    require(dbus(bus, bus_path, bus + ".NameHasOwner", service) == "(false,)",
            "The test bus already has a Secret Service owner")
    with subprocess.Popen(["gnome-keyring-daemon", "--foreground", "--unlock", "--components=secrets",
                           "--control-directory", str(control)], env=keyring_env, stdin=subprocess.PIPE,
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as keyring:
        try:
            keyring.stdin.write(uuid.uuid4().hex.encode("ascii"))
            keyring.stdin.close()
            deadline = time.monotonic() + 15
            while time.monotonic() < deadline:
                require(keyring.poll() is None, "Private Secret Service exited")
                if dbus(bus, bus_path, bus + ".NameHasOwner", service) == "(true,)":
                    require(dbus(bus, bus_path, bus + ".GetConnectionUnixProcessID", service)
                            == f"(uint32 {keyring.pid},)", "Unexpected Secret Service owner")
                    collection = dbus(service, "/org/freedesktop/secrets", "org.freedesktop.Secret.Service.ReadAlias", "default")
                    if collection != "(objectpath '/',)":
                        break
                time.sleep(0.05)
            else:
                raise RuntimeError("Private Secret Service collection never appeared")

            def cli_command(binary, label, *args):
                result = subprocess.run([binary, *args], env=env, cwd=root, capture_output=True, timeout=300)
                require(result.returncode == 0, f"CLI fixture {label} failed with exit {result.returncode}")
                envelope = json.loads(result.stdout)
                require(envelope.get("status") == "ok", f"CLI fixture {label} returned a failure envelope")
                return envelope

            env["RSRS_M3_DIR"] = str(root / "models" / "bge-m3")
            cli_command(seed, "model preparation", "--direct", "model", "install-m3", "--json")
            cli_command(seed, "offline keygen", "--json", "keygen", "--pass", "Isolated-" + uuid.uuid4().hex)
            old_text = "linux-legacy-" + uuid.uuid4().hex
            cli_command(seed, "start", "--json", "runtime", "start")
            try:
                legacy = health()
                require(legacy["store_ready"] and legacy["credential_state"] == "available",
                        "Original host did not open the seed library")
                old_id = cli_command(seed, "remember", "--client-only", "--json", "remember", old_text,
                                      "--title", old_text, "--importance", "important", "--force")["summary"]["id"]
            finally:
                cli_command(seed, "stop", "--json", "runtime", "stop")
            seed.unlink()
            require(shutil.which("rsrs", path=env["PATH"]) is None and assert_runtime_only(),
                    "CLI must be absent before reopening the seed library")
            checks.append("existing_library_cli_fixture_removed")

            def business(owner, command, *args):
                request_id = "linux-handoff-" + uuid.uuid4().hex
                scope = {key: owner[key] for key in ("account_id", "instance_id", "generation")}
                request = urllib.request.Request(f"http://127.0.0.1:{port}/api/rpc",
                    data=json.dumps({"v": 1, "method": "cli.exec", "args": ["--json", command, *args],
                                     "id": request_id, "operation_id": request_id,
                                     "idempotency_key": request_id, "scope": scope}).encode(),
                    headers={"Content-Type": "application/json"})
                with opener.open(request, timeout=60) as response:
                    result = json.load(response)
                require(result["v"] == 1 and result["ok"] and result["exit"] == 0 and result["scope"] == scope
                        and all(result[key] == request_id for key in ("id", "operation_id", "idempotency_key")),
                        f"Runtime {command} receipt differs")
                envelope = result["envelope"]
                require(envelope["status"] == "ok" and envelope["command"] == command, f"Runtime {command} failed")
                return envelope

            text = "linux-handoff-" + uuid.uuid4().hex
            client("start")
            try:
                first = health()
                require(first["store_ready"] and first["credential_state"] == "available"
                        and first["account_id"] == legacy["account_id"] and first["instance_id"] != legacy["instance_id"],
                        "Initial host key handoff failed")
                old_entry = business(first, "show", old_id)["details"]["entry"]
                require(old_entry["id"] == old_id and old_entry["content"] == old_text, "Legacy library readback differs")
                checks.append("legacy_library_readback_without_cli")
                memory_id = business(first, "remember", text, "--title", text, "--importance", "important", "--force")["summary"]["id"]
            finally:
                client("stop")
            client("start")
            try:
                reopened = health()
                require(reopened["store_ready"] and reopened["credential_state"] == "available"
                        and reopened["account_id"] == first["account_id"] and reopened["instance_id"] != first["instance_id"]
                        and reopened["image_sha256"] == first["image_sha256"] and assert_runtime_only(),
                        "Client2 did not reopen the same library without CLI")
                entry = business(reopened, "show", memory_id)["details"]["entry"]
                require(entry["id"] == memory_id and entry["content"] == text, "Reopened library readback differs")
                checks.extend(["scoped_remember_receipt", "native_key_handoff_on_restart_no_cli", "encrypted_library_readback"])
                if "OLDER_CLI_SHA256" in os.environ:
                    selection = Path(env["RSRS_DATA_DIR"]) / "bin/runtime.json"
                    selected = selection.read_bytes()
                    older = root / ("older-" + seed.name)
                    subprocess.run(["gh", "release", "download", "v2.0.0-dev.23", "-R", "risense-ai/respire-cli",
                                    "-p", seed.name, "-O", older], check=True, timeout=90)
                    require(digest(older) == os.environ["OLDER_CLI_SHA256"], "Older online CLI digest differs")
                    older.chmod(0o700)
                    report["online_cli"] = {"version": "2.0.0-dev.23", "sha256": digest(older)}
                    online = cli_command(older, "older online show", "--client-only", "--json", "show", memory_id)["details"]["entry"]
                    require(online["id"] == memory_id and online["content"] == text, "Older online CLI read differs")
                    online_text = "linux-online-" + uuid.uuid4().hex
                    saved = cli_command(older, "older online remember", "--client-only", "--json", "remember",
                                        online_text, "--title", online_text, "--importance", "important", "--force")
                    online_id = saved["summary"]["id"]
                    require(online_id not in (old_id, memory_id), "Older CLI did not create a new entry")
                    roundtrip = business(reopened, "show", online_id)["details"]["entry"]
                    require(roundtrip["id"] == online_id and roundtrip["content"] == online_text, "Older online write differs")
                    current = health()
                    require(all(current[key] == reopened[key] for key in
                                ("pid", "process_start_id", "instance_id", "generation", "account_id", "image_sha256"))
                            and selection.read_bytes() == selected, "Older CLI changed the Client2-owned Runtime")
                    checks.extend(["older_cli_online_read_write", "older_cli_preserved_instance_and_selection"])
            finally:
                client("stop")
        finally:
            if keyring.poll() is None:
                keyring.terminate()
            keyring.wait(timeout=10)


if __name__ == "__main__":
    package_kind = "unpublished PR package" if "CLIENT2_PR_PACKAGE" in os.environ else "sealed package"
    scope = "accountless lifecycle and existing Linux library" if "SEED_CLI" in os.environ else "accountless native lifecycle"
    result = {"status": "failed", "scope": f"{package_kind}, {scope}", "checks": []}
    try:
        check(result)
        result["status"] = "passed"
    finally:
        Path("lifecycle-report.json").write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
