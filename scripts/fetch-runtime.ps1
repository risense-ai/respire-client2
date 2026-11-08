param(
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    [ValidateSet('stable', 'dev')][string]$Channel = 'stable',
    [string]$OutputDir = 'dist/runtime'
)
# Download the managed Runtime (rsrs-runtime.exe + native-library archive) from
# the latest respire-runtime stable release, verified against the published
# runtime-build-<triple>.json metadata, and assert CLI compat. Mirrors the
# validation rules of internal/host LatestRuntime. Requires scripts/fetch-cli.ps1
# to have produced dist/cli/version.txt (the compat assertion reads it).
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$target = @{ 'amd64' = 'x86_64-pc-windows-msvc'; 'arm64' = 'aarch64-pc-windows-msvc' }[$Arch]
$headers = @{
    'User-Agent' = 'respire-client2-installer-build'
    'Accept'     = 'application/vnd.github+json'
}
if ($env:GITHUB_TOKEN) {
    $headers['Authorization'] = "Bearer $($env:GITHUB_TOKEN)"
} else {
    $ghToken = (& gh auth token 2>$null)
    if ($LASTEXITCODE -eq 0 -and $ghToken) { $headers['Authorization'] = "Bearer $ghToken" }
    # gh not being logged in is not a script failure; a stale $LASTEXITCODE
    # here would falsely fail the caller's exit-code check.
    $global:LASTEXITCODE = 0
}

function Invoke-GitHubJson([string]$Url) {
    (Invoke-RestMethod -Uri $Url -Headers $headers -UseBasicParsing)
}
function Invoke-GitHubDownload([string]$Url, [string]$Dest) {
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try {
            Invoke-WebRequest -Uri $Url -Headers $headers -UseBasicParsing -OutFile $Dest
            return
        } catch {
            if ($attempt -eq 3) { throw }
            Start-Sleep -Seconds (3 * $attempt)
        }
    }
}
function Compare-Semver([string]$Left, [string]$Right) {
    # Semver compare including prerelease precedence ("1.0.21-dev.9" < "1.0.21-dev.17" < "1.0.21").
    # Locals must not differ from parameters only by case: $a/$A share one
    # storage slot in PowerShell, and the typed [string] slot would coerce
    # the version array into a space-joined string.
    function Split-Semver([string]$V) {
        $V = ($V.Trim()) -replace '^v', ''
        $core = $V; $pre = ''
        $dash = $V.IndexOf('-')
        if ($dash -ge 0) { $core = $V.Substring(0, $dash); $pre = $V.Substring($dash + 1) }
        $parts = $core.Split('.')
        return @([int]$parts[0], [int]$parts[1], [int]$parts[2], $pre)
    }
    $a = Split-Semver $Left
    $b = Split-Semver $Right
    for ($i = 0; $i -lt 3; $i++) {
        if ($a[$i] -lt $b[$i]) { return -1 }
        if ($a[$i] -gt $b[$i]) { return 1 }
    }
    if ($a[3] -eq '' -and $b[3] -eq '') { return 0 }
    if ($a[3] -eq '') { return 1 }
    if ($b[3] -eq '') { return -1 }
    $ai = $a[3].Split('.'); $bi = $b[3].Split('.')
    for ($i = 0; $i -lt [Math]::Max($ai.Count, $bi.Count); $i++) {
        $x = if ($i -lt $ai.Count) { $ai[$i] } else { '' }
        $y = if ($i -lt $bi.Count) { $bi[$i] } else { '' }
        $xn = 0; $yn = 0
        $xIsNum = [int]::TryParse($x, [ref]$xn); $yIsNum = [int]::TryParse($y, [ref]$yn)
        if ($xIsNum -and $yIsNum) {
            if ($xn -lt $yn) { return -1 }
            if ($xn -gt $yn) { return 1 }
        } elseif ($xIsNum -ne $yIsNum) {
            if ($xIsNum) { return -1 } else { return 1 }
        } elseif ($x -ne $y) {
            if ($x -lt $y) { return -1 } else { return 1 }
        }
    }
    return 0
}
function Test-VersionInRange([string]$Version, [string]$Min, [string]$Max) {
    return ((Compare-Semver $Min $Version) -le 0) -and ((Compare-Semver $Version $Max) -le 0)
}

Push-Location $repoRoot
try {
    $cliVersion = (Get-Content (Join-Path $repoRoot 'dist/cli/version.txt') -Raw).Trim()
    if (-not $cliVersion) { throw 'dist/cli/version.txt missing - run scripts/fetch-cli.ps1 first' }

    if ($Channel -eq 'stable') {
        $release = Invoke-GitHubJson 'https://api.github.com/repos/risense-ai/respire-runtime/releases/latest'
        if (-not $release.tag_name) { throw 'releases/latest returned no tag' }
        if ($release.prerelease -or $release.tag_name -match '-') {
            throw "stable channel returned a prerelease: $($release.tag_name)"
        }
    } else {
        # Dev channel mirrors the client: list recent releases, drop drafts,
        # pick the highest semver (prereleases allowed).
        $list = Invoke-GitHubJson 'https://api.github.com/repos/risense-ai/respire-runtime/releases?per_page=20'
        $release = $null
        foreach ($r in $list) {
            if ($r.draft -or -not $r.tag_name) { continue }
            $v = $r.tag_name -replace '^v', ''
            if ($v -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { continue }
            if (-not $release -or ((Compare-Semver $v ($release.tag_name -replace '^v', '')) -gt 0)) { $release = $r }
        }
        if (-not $release) { throw 'dev channel returned no usable release' }
    }

    $metaAsset = $release.assets | Where-Object { $_.name -eq "runtime-build-$target.json" } | Select-Object -First 1
    if (-not $metaAsset) { throw "runtime-build-$target.json not found in $($release.tag_name) assets" }
    $binaryAsset = $release.assets | Where-Object { $_.name -eq "rsrs-runtime-$target.exe" } | Select-Object -First 1
    if (-not $binaryAsset) { throw "rsrs-runtime-$target.exe not found in $($release.tag_name) assets" }
    $archiveAsset = $release.assets | Where-Object { $_.name -eq "rsrs-runtime-$target.tar.gz" } | Select-Object -First 1
    if (-not $archiveAsset) { throw "rsrs-runtime-$target.tar.gz not found in $($release.tag_name) assets" }

    $tempBase = if ($env:RUNNER_TEMP -and (Test-Path $env:RUNNER_TEMP)) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
    $tmp = Join-Path $tempBase "respire-runtime-fetch-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $tmp | Out-Null

    try {
        $metaPath = Join-Path $tmp 'runtime-build.json'
        Invoke-GitHubDownload $metaAsset.browser_download_url $metaPath
        $meta = Get-Content $metaPath -Raw | ConvertFrom-Json
        # Accept schema v1 (triple/sha256) and v2 (target_triple/archive_sha256)
        # keys; the runtime repo currently publishes v2.
        if ($meta.schema_version -notin @(1, 2)) { throw "unsupported runtime metadata schema: $($meta.schema_version)" }
        $metaTarget = if ($meta.target_triple) { $meta.target_triple } else { $meta.triple }
        $metaArchiveHash = if ($meta.archive_sha256) { $meta.archive_sha256 } else { $meta.sha256 }
        if (-not $metaTarget) { throw 'metadata missing target' }
        if ($meta.runtime_version -ne ($release.tag_name -replace '^v', '')) { throw "runtime_version mismatch: $($meta.runtime_version)" }
        if ($meta.tag -ne $release.tag_name) { throw "tag mismatch: $($meta.tag) != $($release.tag_name)" }
        if ($metaTarget -ne $target) { throw "metadata target mismatch: $metaTarget != $target" }
        if ($meta.binary_file -ne "rsrs-runtime-$target.exe") { throw "metadata binary_file mismatch: $($meta.binary_file)" }
        if (-not $meta.binary_sha256 -or -not $metaArchiveHash) { throw 'metadata missing hashes' }
        $maxCli = if ($meta.compat.max_cli -eq '*') { $cliVersion } else { $meta.compat.max_cli }
        if (-not (Test-VersionInRange $cliVersion $meta.compat.min_cli $maxCli)) {
            throw "bundled CLI $cliVersion outside Runtime compat range [$($meta.compat.min_cli), $($meta.compat.max_cli)]"
        }
        $expectedArchiveHash = $metaArchiveHash.ToLowerInvariant()

        $outDir = Join-Path $repoRoot $OutputDir
        $treeDir = Join-Path $outDir $meta.runtime_version
        # The client expects the binary as rsrs-runtime.exe inside the tree
        # (runtimeBinary() in internal/host).
        $cached = (Test-Path (Join-Path $treeDir 'rsrs-runtime.exe')) -and (Test-Path (Join-Path $treeDir 'core-runtime.json')) -and (Test-Path (Join-Path $outDir 'sha256.txt'))
        if ($cached -and ((Get-Content (Join-Path $outDir 'sha256.txt') -Raw).Trim() -eq $meta.binary_sha256.ToLowerInvariant())) {
            Write-Output "reusing cached rsrs-runtime $($meta.runtime_version) ($target), sha256 verified"
            return
        }

        $binPath = Join-Path $tmp "rsrs-runtime-$target.exe"
        $archivePath = Join-Path $tmp 'runtime.tar.gz'
        Invoke-GitHubDownload $binaryAsset.browser_download_url $binPath
        Invoke-GitHubDownload $archiveAsset.browser_download_url $archivePath
        $binHash = (Get-FileHash -LiteralPath $binPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($binHash -ne $meta.binary_sha256.ToLowerInvariant()) { throw "binary sha256 mismatch: $binHash != $($meta.binary_sha256)" }
        $archHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($archHash -ne $expectedArchiveHash) { throw "archive sha256 mismatch: $archHash != $expectedArchiveHash" }

        # Extract with the system bsdtar; Git Bash's GNU tar treats C:/ paths as
        # remote hosts, so always pin the absolute System32 path.
        $tar = Join-Path $env:SystemRoot 'System32\tar.exe'
        if (-not (Test-Path $tar)) { throw "system tar not found: $tar" }
        if (Test-Path $treeDir) { Remove-Item -LiteralPath $treeDir -Recurse -Force }
        New-Item -ItemType Directory -Force -Path $treeDir | Out-Null
        & $tar -xzf $archivePath -C $treeDir
        if ($LASTEXITCODE -ne 0) { throw 'archive extraction failed' }
        Copy-Item -LiteralPath $binPath -Destination (Join-Path $treeDir 'rsrs-runtime.exe') -Force

        [IO.File]::WriteAllText((Join-Path $outDir 'version.txt'), "$($meta.runtime_version)", (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $outDir 'sha256.txt'), "$($meta.binary_sha256.ToLowerInvariant())", (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $outDir 'channel.txt'), "$Channel", (New-Object Text.UTF8Encoding($false)))
        Write-Output "fetched rsrs-runtime $($meta.runtime_version) ($target, $Channel), binary+archive sha256 verified, CLI $cliVersion within compat [$($meta.compat.min_cli), $($meta.compat.max_cli)]"
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
} finally {
    Pop-Location
}

exit 0
