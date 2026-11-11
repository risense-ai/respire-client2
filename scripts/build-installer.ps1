param(
    [Parameter(Mandatory)][string]$Version,
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64'
)
# Compile only the desktop application; CLI and Runtime install independently.
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
