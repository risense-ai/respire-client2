param([string]$Version = '0.2.7', [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64')
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
    $env:GOARCH = $Arch
    $artifactName = "respire-windows-$Arch.exe"
    $fileVersion = ($Version -split '-')[0] + '.0'
    $env:GOARCH = (go env GOHOSTARCH).Trim()
    go run github.com/tc-hib/go-winres@v0.3.3 make --in build/winres.json --out rsrc --arch $Arch --file-version $fileVersion --product-version $fileVersion
    if ($LASTEXITCODE -ne 0) { throw 'Windows resource generation failed' }
    $env:GOARCH = $Arch
    go vet -tags production ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go static analysis failed' }
    Get-ChildItem internal/desktop/frontend -Recurse -Filter *.js -File | ForEach-Object {
        node --check $_.FullName
        if ($LASTEXITCODE -ne 0) { throw "Frontend syntax check failed: $($_.Name)" }
    }
    go build -tags production -trimpath -ldflags "-s -w -H windowsgui -X main.version=$Version" -o "dist/$artifactName" .
    if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
    Copy-Item -LiteralPath LICENSE -Destination dist/LICENSE.txt
    Copy-Item -LiteralPath internal/desktop/frontend/notices.txt -Destination dist/THIRD_PARTY_NOTICES.txt
    $binaryHash = (Get-FileHash -LiteralPath "dist/$artifactName" -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksum = "$binaryHash  $artifactName`n"
    [IO.File]::WriteAllText((Join-Path $repoRoot 'dist\SHA256SUMS'), $checksum, (New-Object Text.UTF8Encoding($false)))
    Get-Item -LiteralPath "dist/$artifactName" | Select-Object Name, Length
} finally {
    Pop-Location
}
