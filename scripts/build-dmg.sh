#!/usr/bin/env bash
# Build the Apple Silicon desktop DMG, verify its signature and append its hash.
# CLI and Runtime are downloaded independently at first use.
set -euo pipefail
cd "$(dirname "$0")/.."

app="dist/Respire.app"
dmg="dist/respire-darwin-arm64.dmg"
volname="Respire"

[[ "$(uname -s)" == Darwin ]] || { echo "DMG builds need macOS (hdiutil)" >&2; exit 1; }
[[ -d "$app" ]] || { echo "$app is missing; run scripts/build.sh first" >&2; exit 1; }
[[ ! -e "$app/Contents/Resources/bootstrap" ]] || {
    echo 'DMG must not bundle CLI or Runtime components' >&2
    exit 1
}
if file "$app/Contents/MacOS/respire" | grep -qv arm64; then
    echo "the bundle binary is not arm64; the full DMG is Apple Silicon only" >&2
    exit 1
fi
codesign --verify --deep --strict "$app"

staging=$(mktemp -d)
mountpoint=$(mktemp -d)
trap 'hdiutil detach "$mountpoint" -force >/dev/null 2>&1 || true; rm -rf "$staging" "$mountpoint"' EXIT

cp -R "$app" "$staging/Respire.app"
ln -s /Applications "$staging/Applications"

rm -f "$dmg"
hdiutil create -volname "$volname" -srcfolder "$staging" -ov -format UDZO "$dmg" >/dev/null

# Mount-verify-unmount: volume contents, symlink target, and the signature
# of the app as shipped inside the image.
hdiutil attach -readonly -nobrowse -mountpoint "$mountpoint" "$dmg" >/dev/null
[[ -d "$mountpoint/Respire.app" ]] || { echo "mounted volume is missing Respire.app" >&2; exit 1; }
[[ -L "$mountpoint/Applications" ]] || { echo "mounted volume is missing the /Applications symlink" >&2; exit 1; }
[[ "$(readlink "$mountpoint/Applications")" == /Applications ]] || { echo "Applications symlink must target /Applications" >&2; exit 1; }
[[ ! -e "$mountpoint/Respire.app/Contents/Resources/bootstrap" ]] || { echo "DMG contains bundled components" >&2; exit 1; }
codesign --verify --deep --strict "$mountpoint/Respire.app"
hdiutil detach "$mountpoint" >/dev/null

(cd dist && shasum -a 256 "respire-darwin-arm64.dmg") >> dist/SHA256SUMS
# Re-sign the checksums file listing so zip and DMG hashes sit side by side.
grep -q "respire-darwin-arm64.zip" dist/SHA256SUMS || {
    echo "dist/SHA256SUMS lost the zip line; refusing to leave a partial manifest" >&2
    exit 1
}
echo "DMG ready: $dmg" >&2
