#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-0.1.0-dev.0}"
arch="${2:-$(go env GOARCH)}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Invalid semantic version' >&2; exit 1; }
goos="$(go env GOOS)"
case "$goos" in linux|darwin) ;; *) echo 'Use build.ps1 on Windows' >&2; exit 1;; esac
export GOMODCACHE="$PWD/.tools/mod" GOCACHE="$PWD/.tools/cache" GOPATH="$PWD/.tools/go"
export GOARCH="$arch" CGO_ENABLED=1
tags=production
[[ "$goos" != linux ]] || tags=production,gtk3
mkdir -p dist
go vet -tags "$tags" ./...
if command -v node >/dev/null 2>&1; then node --check internal/desktop/frontend/app.js; fi
go build -tags "$tags" -trimpath -ldflags "-s -w -X main.version=$version" -o dist/respire .
file dist/respire
if [[ "$goos" == darwin ]]; then
    bundle=dist/Respire.app
    mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"
    cp dist/respire "$bundle/Contents/MacOS/respire"
    cp LICENSE "$bundle/Contents/Resources/LICENSE.txt"
    cp internal/desktop/frontend/notices.txt "$bundle/Contents/Resources/THIRD_PARTY_NOTICES.txt"
    iconset=.tools/respire.iconset
    mkdir -p "$iconset"
    for size in 16 32 128 256 512; do
        sips -z "$size" "$size" internal/desktop/build/icon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
        doubled=$((size * 2))
        sips -z "$doubled" "$doubled" internal/desktop/build/icon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
    done
    iconutil -c icns "$iconset" -o "$bundle/Contents/Resources/respire.icns"
    cat > "$bundle/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>ai.risense.respire.client2</string>
<key>CFBundleName</key><string>Respire</string>
<key>CFBundleDisplayName</key><string>Respire</string>
<key>CFBundleExecutable</key><string>respire</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleIconFile</key><string>respire.icns</string>
<key>CFBundleShortVersionString</key><string>${version%%-*}</string>
<key>CFBundleVersion</key><string>${version%%-*}</string>
<key>LSMinimumSystemVersion</key><string>11.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
    codesign --force --deep --sign - "$bundle"
    codesign --verify --deep --strict "$bundle"
    artifact="respire-darwin-$arch.zip"
    ditto -c -k --sequesterRsrc --keepParent "$bundle" "dist/$artifact"
    (cd dist && shasum -a 256 "$artifact") > dist/SHA256SUMS
else
    artifact="respire-linux-$arch.tar.gz"
    tar -czf "dist/$artifact" -C dist respire
    (cd dist && sha256sum "$artifact") > dist/SHA256SUMS
fi
cp LICENSE dist/LICENSE.txt
cp internal/desktop/frontend/notices.txt dist/THIRD_PARTY_NOTICES.txt
