#!/usr/bin/env python3
import base64
import hashlib
import json
import pathlib
import sys
from datetime import datetime, timezone


tag, dist, notes, output = sys.argv[1:5]
# Only the six application update assets enter this manifest. Standalone
# installers remain release downloads and are included in SHA256SUMS.
expected = int(sys.argv[5]) if len(sys.argv) > 5 else 6
if not tag.startswith("v"):
    raise SystemExit("tag must start with v")
version = tag[1:]
folder = pathlib.Path(dist)
checksums = {}
for line in (folder / "SHA256SUMS").read_text().splitlines():
    digest, name = line.split(None, 1)
    name = name.removeprefix("*").strip()
    if name in checksums or pathlib.Path(name).name != name:
        raise SystemExit("duplicate or unsafe checksum filename")
    if hashlib.sha256((folder / name).read_bytes()).hexdigest() != digest.lower():
        raise SystemExit(f"checksum differs: {name}")
    checksums[name] = digest.lower()

targets = {f"respire-{system}-{arch}.{suffix}"
           for system, suffix in [("windows", "exe"), ("darwin", "zip"), ("linux", "tar.gz")]
           for arch in ["amd64", "arm64"]}
if expected != 6 or set(checksums) != targets | {
        "respire-windows-amd64-setup.exe", "respire-windows-arm64-setup.exe", "respire-darwin-arm64.dmg"}:
    raise SystemExit("expected exactly six desktop targets, two installers and one DMG")

artifacts = []
for name, digest in sorted(checksums.items()):
    if not name.startswith("respire-") or name.endswith(".dmg") or name.endswith("-setup.exe"):
        continue
    artifacts.append({
        "filename": name,
        "url": f"https://github.com/risense-ai/respire-client2/releases/download/{tag}/{name}",
        "size": (folder / name).stat().st_size,
        "sha256": base64.b64encode(bytes.fromhex(digest)).decode(),
    })
if len(artifacts) != expected:
    raise SystemExit(f"expected {expected} platform artifacts, found {len(artifacts)}")

release = {
    "version": version,
    "notes": pathlib.Path(notes).read_text(),
    "publishedAt": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
    "artifacts": artifacts,
}
manifest = {"schemaVersion": 1, "stable": release}
if "-" in version:
    manifest = {"schemaVersion": 1, "dev": release}
else:
    manifest["dev"] = release
pathlib.Path(output).write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n")
