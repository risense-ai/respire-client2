#!/usr/bin/env python3
import base64
import json
import pathlib
import sys
from datetime import datetime, timezone


tag, dist, notes, output = sys.argv[1:]
if not tag.startswith("v"):
    raise SystemExit("tag must start with v")
version = tag[1:]
folder = pathlib.Path(dist)
checksums = {}
for line in (folder / "SHA256SUMS").read_text().splitlines():
    digest, name = line.split(None, 1)
    checksums[name.removeprefix("*").strip()] = digest.lower()

artifacts = []
for name, digest in sorted(checksums.items()):
    if not name.startswith("respire-"):
        continue
    artifacts.append({
        "filename": name,
        "url": f"https://github.com/risense-ai/respire-client2/releases/download/{tag}/{name}",
        "size": (folder / name).stat().st_size,
        "sha256": base64.b64encode(bytes.fromhex(digest)).decode(),
    })
if len(artifacts) != 6:
    raise SystemExit(f"expected six platform artifacts, found {len(artifacts)}")

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
