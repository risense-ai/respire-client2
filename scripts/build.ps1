param([string]$Version = '0.1.0-dev.0')
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw 'Use a semantic version without a v prefix' }
$repoRoot = Split-Path -Parent $PSScriptRoot
Push-Location $repoRoot
try {
    $env:GOMODCACHE = Join-Path $repoRoot '.tools\mod'
    $env:GOCACHE = Join-Path $repoRoot '.tools\cache'
    $env:GOPATH = Join-Path $repoRoot '.tools\go'
    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $fileVersion = ($Version -split '-')[0] + '.0'
    go run github.com/tc-hib/go-winres@v0.3.3 make --in build/winres.json --out rsrc --arch amd64 --file-version $fileVersion --product-version $fileVersion
    if ($LASTEXITCODE -ne 0) { throw 'Windows resource generation failed' }
    go vet -tags production ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go static analysis failed' }
    node --check internal/desktop/frontend/app.js
    if ($LASTEXITCODE -ne 0) { throw 'Frontend syntax check failed' }
    go build -tags production -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o dist/respire-windows-amd64.exe .
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
    Copy-Item -LiteralPath LICENSE -Destination dist/LICENSE.txt
    Copy-Item -LiteralPath internal/desktop/frontend/notices.txt -Destination dist/THIRD_PARTY_NOTICES.txt
    $binaryHash = (Get-FileHash -LiteralPath dist/respire-windows-amd64.exe -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksum = "$binaryHash  respire-windows-amd64.exe`n"
    [IO.File]::WriteAllText((Join-Path $repoRoot 'dist\SHA256SUMS'), $checksum, (New-Object Text.UTF8Encoding($false)))
    Get-Item -LiteralPath dist/respire-windows-amd64.exe | Select-Object Name, Length
} finally {
    Pop-Location
}
