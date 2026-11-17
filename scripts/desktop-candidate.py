#!/usr/bin/env python3
"""Bind native package checks and publication to one immutable desktop candidate."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import zipfile


TARGETS = {
    "windows-amd64": ["respire-windows-amd64.exe", "respire-windows-amd64-setup.exe"],
    "windows-arm64": ["respire-windows-arm64.exe", "respire-windows-arm64-setup.exe"],
    "darwin-amd64": ["respire-darwin-amd64.zip"],
    "darwin-arm64": ["respire-darwin-arm64.zip", "respire-darwin-arm64.dmg"],
    "linux-amd64": ["respire-linux-amd64.tar.gz"],
    "linux-arm64": ["respire-linux-arm64.tar.gz"],
}
ASSETS = {name for names in TARGETS.values() for name in names}
FILES = ASSETS | {"SHA256SUMS", "LICENSE.txt", "THIRD_PARTY_NOTICES.txt",
                  "client-updates.json", "release-notes.md"}
SCOPE = "package_checks"
SHA = re.compile(r"[0-9a-f]{40}")
VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    require(path.is_file() and not path.is_symlink(), f"Not a regular candidate file: {path}")
    hashed = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            hashed.update(block)
    return hashed.hexdigest()


def read(path):
    return json.loads(path.read_text(encoding="utf-8"))


def write(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8", newline="\n")


def gh(*args):
    return json.loads(subprocess.check_output(["gh", "api", *args], text=True))


def inputs():
    mode = os.environ.get("INPUT_MODE", "candidate")
    version = os.environ.get("INPUT_VERSION", "")
    source = os.environ["GITHUB_SHA"]
    if os.environ["GITHUB_REF"].startswith("refs/tags/v"):
        mode, version = "tag", os.environ["GITHUB_REF_NAME"][1:]
        subprocess.run(["git", "merge-base", "--is-ancestor", source, "origin/main"], check=True)
    else:
        require(os.environ["GITHUB_EVENT_NAME"] == "workflow_dispatch"
                and os.environ["GITHUB_REF"] == "refs/heads/main", "Dispatch must use trusted main")
        require(mode in {"candidate", "promote"}, "Unknown desktop action")
    require(VERSION.fullmatch(version), "An explicit semantic version without v is required")
    if mode == "promote":
        require(os.environ.get("INPUT_CONFIRM") == "true", "Promotion requires explicit publication confirmation")
        source = os.environ.get("INPUT_SOURCE_SHA", "")
        for key in ["INPUT_RUN_ID", "INPUT_ATTEMPT"]:
            require(re.fullmatch(r"[1-9][0-9]*", os.environ.get(key, "")), f"Missing producer {key}")
    require(SHA.fullmatch(source), "Expected the full immutable source SHA")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"mode={mode}\nversion={version}\nsource={source}\n")


def producer(root):
    repo = os.environ["GITHUB_REPOSITORY"]
    run_id, attempt = os.environ["INPUT_RUN_ID"], int(os.environ["INPUT_ATTEMPT"])
    run = gh(f"repos/{repo}/actions/runs/{run_id}/attempts/{attempt}")
    workflow = gh(f"repos/{repo}/actions/workflows/desktop.yml")
    require(run["repository"]["full_name"] == run["head_repository"]["full_name"] == repo
            and run["workflow_id"] == workflow["id"] and run["event"] == "workflow_dispatch"
            and run["head_branch"] == "main" and run["head_sha"] == os.environ["CANDIDATE_SOURCE"]
            and run["run_attempt"] == attempt and run["status"] == "completed"
            and run["conclusion"] == "success", "Producer is not the successful trusted main attempt/source")
    name = f"desktop-package-checks-{attempt}"
    pages = gh("--paginate", "--slurp", f"repos/{repo}/actions/runs/{run_id}/artifacts?per_page=100")
    artifacts = [artifact for page in pages for artifact in page["artifacts"] if artifact["name"] == name]
    require(len(artifacts) == 1, "Exactly one sealed package-check artifact required")
    artifact = artifacts[0]
    require(not artifact["expired"] and re.fullmatch(r"sha256:[0-9a-f]{64}", artifact["digest"])
            and artifact["workflow_run"]["id"] == int(run_id)
            and artifact["workflow_run"]["head_sha"] == run["head_sha"], "Artifact producer/digest differs")
    archive = Path("candidate-artifact.zip")
    with archive.open("wb") as output:
        subprocess.run(["gh", "api", f"repos/{repo}/actions/artifacts/{artifact['id']}/zip"], stdout=output, check=True)
    require("sha256:" + digest(archive) == artifact["digest"], "Downloaded artifact ZIP digest differs")
    require(not root.exists(), "Refusing a reused promotion directory")
    with zipfile.ZipFile(archive) as zipped:
        entries = zipped.infolist()
        require(len(entries) == len(FILES) + 2 and {e.filename for e in entries}
                == FILES | {"candidate.json", "package-checks.json"}, "Unexpected archive file set or duplicate path")
        require(all(not stat.S_ISLNK(e.external_attr >> 16) for e in entries), "Artifact contains symlinks")
        zipped.extractall(root)
    verify(root, True)
    print(json.dumps({"artifact_id": artifact["id"], "artifact_zip_digest": artifact["digest"],
                      "producer": read(root / "candidate.json")["producer"]}))


def verify(root, sealed=False):
    manifest = read(root / "candidate.json")
    expected_producer = {"repository": os.environ["GITHUB_REPOSITORY"],
                         "run_id": os.environ.get("INPUT_RUN_ID") or os.environ["GITHUB_RUN_ID"],
                         "attempt": int(os.environ.get("INPUT_ATTEMPT") or os.environ["GITHUB_RUN_ATTEMPT"])}
    require(manifest["schema_version"] == 1 and manifest["scope"] == SCOPE
            and manifest["source"] == os.environ["CANDIDATE_SOURCE"]
            and manifest["version"] == os.environ["CANDIDATE_VERSION"]
            and manifest["producer"] == expected_producer and manifest["targets"] == TARGETS,
            "Candidate source/version/producer/six-target binding differs")
    require(set(manifest["files"]) == FILES, "Candidate must contain exactly nine product assets and release metadata")
    require({p.name for p in root.iterdir()} == FILES | {"candidate.json"}
            | ({"package-checks.json"} if sealed else set()), "Unexpected or missing candidate files")
    for name, info in manifest["files"].items():
        require(info == {"sha256": digest(root / name), "size": (root / name).stat().st_size},
                f"Candidate bytes changed: {name}")
    sums = (root / "SHA256SUMS").read_text().splitlines()
    require(len(sums) == len(ASSETS) and set(sums) == {
        f"{manifest['files'][name]['sha256']}  {name}" for name in ASSETS}, "Product checksum set differs")
    update = read(root / "client-updates.json")
    release = update["dev" if "-" in manifest["version"] else "stable"]
    expected_updates = [{"filename": names[0],
        "url": f"https://github.com/{expected_producer['repository']}/releases/download/v{manifest['version']}/{names[0]}",
        "size": manifest["files"][names[0]]["size"],
        "sha256": base64.b64encode(bytes.fromhex(manifest["files"][names[0]]["sha256"])).decode()}
        for names in TARGETS.values()]
    require(update["schemaVersion"] == 1 and release["version"] == manifest["version"]
            and release["notes"] == (root / "release-notes.md").read_text(encoding="utf-8")
            and sorted(release["artifacts"], key=lambda a: a["filename"])
            == sorted(expected_updates, key=lambda a: a["filename"]), "Six-target update manifest differs")
    if sealed:
        check_receipts(root, read(root / "package-checks.json"))
    return manifest


def check_receipts(root, receipts):
    require(isinstance(receipts, list) and len(receipts) == len(TARGETS), "Six native package receipts required")
    require({r["target"] for r in receipts} == set(TARGETS), "Missing or repeated native target receipt")
    manifest = read(root / "candidate.json")
    for receipt in receipts:
        require(receipt == {"scope": SCOPE, "target": receipt["target"], "status": "passed",
            "candidate_sha256": digest(root / "candidate.json"), "producer": manifest["producer"]},
            "Native package receipt is not bound to this candidate/attempt")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["inputs", "producer", "manifest", "verify", "receipt", "seal", "tag"])
    parser.add_argument("--root", type=Path, default=Path("dist"))
    parser.add_argument("--receipts", type=Path, default=Path("package-checks"))
    parser.add_argument("--target", choices=TARGETS)
    parser.add_argument("--sealed", action="store_true")
    args = parser.parse_args()
    if args.action == "inputs":
        inputs()
    elif args.action == "producer":
        producer(args.root)
    elif args.action == "manifest":
        require(subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()
                == os.environ["CANDIDATE_SOURCE"], "Assembly checkout differs from producer source")
        require({p.name for p in args.root.iterdir()} == FILES, "Assembly has unexpected or missing files")
        write(args.root / "candidate.json", {"schema_version": 1, "scope": SCOPE,
            "source": os.environ["CANDIDATE_SOURCE"], "version": os.environ["CANDIDATE_VERSION"],
            "producer": {"repository": os.environ["GITHUB_REPOSITORY"], "run_id": os.environ["GITHUB_RUN_ID"],
                         "attempt": int(os.environ["GITHUB_RUN_ATTEMPT"])}, "targets": TARGETS,
            "files": {name: {"sha256": digest(args.root / name), "size": (args.root / name).stat().st_size}
                      for name in sorted(FILES)}})
        verify(args.root)
    elif args.action == "receipt":
        manifest = verify(args.root)
        require(args.target, "Native target required")
        args.receipts.mkdir(parents=True, exist_ok=True)
        write(args.receipts / f"{args.target}.json", {"scope": SCOPE, "target": args.target, "status": "passed",
            "candidate_sha256": digest(args.root / "candidate.json"), "producer": manifest["producer"]})
    elif args.action == "seal":
        verify(args.root)
        require({p.name for p in args.receipts.iterdir()} == {f"{target}.json" for target in TARGETS},
                "Exactly six package check receipts required")
        receipts = [read(args.receipts / f"{target}.json") for target in TARGETS]
        check_receipts(args.root, receipts)
        write(args.root / "package-checks.json", receipts)
        verify(args.root, True)
    elif args.action == "tag":
        verify(args.root, True)
        repo, source = os.environ["GITHUB_REPOSITORY"], os.environ["CANDIDATE_SOURCE"]
        tag = "v" + os.environ["CANDIDATE_VERSION"]
        refs = gh(f"repos/{repo}/git/matching-refs/tags/{tag}")
        refs = [ref for ref in refs if ref["ref"] == "refs/tags/" + tag]
        if not refs:
            gh("--method", "POST", f"repos/{repo}/git/refs", "-f", "ref=refs/tags/" + tag, "-f", "sha=" + source)
        else:
            obj = refs[0]["object"]
            while obj["type"] == "tag":
                obj = gh(f"repos/{repo}/git/tags/{obj['sha']}")["object"]
            require(obj["type"] == "commit" and obj["sha"] == source, "Existing release tag points to different source")
    else:
        verify(args.root, args.sealed)


if __name__ == "__main__":
    main()
