param(
    [Parameter(Mandatory)][string]$Version,
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    [ValidateSet('stable', 'dev')][string]$Channel = 'stable'
)
# Compile the Windows installer: verifies dist inputs exist, fetches+verifies
# the bundled rsrs.exe, then compiles scripts/setup.iss with ISCC.
# Prerequisite: scripts/build.ps1 has produced the per-arch dist inputs.
$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$') { throw 'Use a semantic version without a v prefix' }
$repoRoot = Split-Path -Parent $PSScriptRoot

$iscc = @(
    (Join-Path $env:LOCALAPPDATA 'Programs\Inno Setup 6\ISCC.exe'),
    (Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'),
    (Join-Path $env:ProgramFiles 'Inno Setup 6\ISCC.exe')
) | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
if (-not $iscc) { throw 'Inno Setup 6 not found (ISCC.exe); install with: winget install JRSoftware.InnoSetup' }

foreach ($f in "dist\respire-windows-$Arch.exe", 'dist\LICENSE.txt', 'dist\THIRD_PARTY_NOTICES.txt') {
    if (-not (Test-Path (Join-Path $repoRoot $f))) { throw "missing $f - run scripts/build.ps1 -Version $Version -Arch $Arch first" }
}

& (Join-Path $PSScriptRoot 'fetch-cli.ps1') -Arch $Arch -Channel $Channel
if ($LASTEXITCODE -ne 0) { throw 'fetch-cli.ps1 failed' }
$cliVersion = (Get-Content (Join-Path $repoRoot 'dist\cli\version.txt') -Raw).Trim()

# Same channel for CLI and Runtime keeps the compat assertion meaningful.
& (Join-Path $PSScriptRoot 'fetch-runtime.ps1') -Arch $Arch -Channel $Channel
if ($LASTEXITCODE -ne 0) { throw 'fetch-runtime.ps1 failed' }
$rtVersion = (Get-Content (Join-Path $repoRoot 'dist\runtime\version.txt') -Raw).Trim()
$rtSha256 = (Get-Content (Join-Path $repoRoot 'dist\runtime\sha256.txt') -Raw).Trim()
$rtMetadata = Get-Content (Join-Path $repoRoot 'dist\runtime\runtime-build.json') -Raw | ConvertFrom-Json
$cliPath = Join-Path $repoRoot 'dist\cli\rsrs.exe'
$cliHash = (Get-FileHash -LiteralPath $cliPath -Algorithm SHA256).Hash.ToLowerInvariant()
$bootstrap = Join-Path $repoRoot 'dist\bootstrap'
# Each architecture builds in its own CI job. Refuse a stale payload rather
# than merging different component sets into one installation package.
if (Test-Path -LiteralPath $bootstrap) { throw 'dist/bootstrap already exists; use a fresh build directory' }
New-Item -ItemType Directory -Path $bootstrap | Out-Null
Copy-Item -LiteralPath (Join-Path $repoRoot "dist\runtime\$rtVersion") -Destination (Join-Path $bootstrap 'runtime') -Recurse
Copy-Item -LiteralPath $cliPath -Destination (Join-Path $bootstrap 'rsrs.exe')
$target = @{ 'amd64' = 'x86_64-pc-windows-msvc'; 'arm64' = 'aarch64-pc-windows-msvc' }[$Arch]
$manifest = [ordered]@{
    schema_version = 1; target = $target; channel = $Channel
    cli = [ordered]@{ version = $cliVersion; sha256 = $cliHash }
    runtime = [ordered]@{
        version = $rtVersion; sha256 = $rtSha256
        min_cli = $rtMetadata.compat.min_cli; max_cli = $rtMetadata.compat.max_cli
    }
}
[IO.File]::WriteAllText((Join-Path $bootstrap 'bootstrap.json'), (($manifest | ConvertTo-Json -Depth 4) + "`n"), (New-Object Text.UTF8Encoding($false)))

Push-Location $repoRoot
try {
    & $iscc "/DAppVersion=$Version" "/DArch=$Arch" "/DOutputBaseName=respire-windows-$Arch-setup" (Join-Path $repoRoot 'scripts\setup.iss')
    if ($LASTEXITCODE -ne 0) { throw 'ISCC compilation failed' }
} finally {
    Pop-Location
}
$out = Join-Path $repoRoot "dist\respire-windows-$Arch-setup.exe"
if (-not (Test-Path $out)) { throw "installer output missing: $out" }
$artifactName = "respire-windows-$Arch-setup.exe"
$checksum = (Get-FileHash -LiteralPath $out -Algorithm SHA256).Hash.ToLowerInvariant()
# Rewrite: drop any previous entry for this artifact so rebuilds never duplicate.
$sumsPath = Join-Path $repoRoot 'dist\SHA256SUMS'
$lines = @(Get-Content $sumsPath -ErrorAction SilentlyContinue | Where-Object { $_ -and -not ($_.TrimEnd() -match [regex]::Escape($artifactName) + '$') })
$lines += "$checksum  $artifactName"
[IO.File]::WriteAllText($sumsPath, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding($false)))
Get-Item -LiteralPath $out | Select-Object Name, Length
