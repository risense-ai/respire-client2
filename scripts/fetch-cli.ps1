param(
    [ValidateSet('amd64', 'arm64')][string]$Arch = 'amd64',
    [ValidateSet('stable', 'dev')][string]$Channel = 'stable',
    [string]$OutputDir = 'dist/cli'
)
# Download the matching rsrs.exe from the latest respire-cli stable release,
# verified against the published cli-build-<triple>.json metadata. Fails the
# build on prerelease, missing target or hash mismatch - no fallbacks.
$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
$target = @{ 'amd64' = 'x86_64-pc-windows-msvc'; 'arm64' = 'aarch64-pc-windows-msvc' }[$Arch]
$headers = @{
    'User-Agent'   = 'respire-client2-installer-build'
    'Accept'       = 'application/vnd.github+json'
}
if ($env:GITHUB_TOKEN) {
    $headers['Authorization'] = "Bearer $($env:GITHUB_TOKEN)"
} else {
    # Local builds fall back to the gh CLI token; CI injects GITHUB_TOKEN.
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
    # Semver compare including prerelease precedence; see fetch-runtime.ps1.
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

Push-Location $repoRoot
try {
    if ($Channel -eq 'stable') {
        $release = Invoke-GitHubJson 'https://api.github.com/repos/risense-ai/respire-cli/releases/latest'
        if (-not $release.tag_name) { throw 'releases/latest returned no tag' }
        if ($release.prerelease -or $release.tag_name -match '-') {
            throw "stable channel returned a prerelease: $($release.tag_name)"
        }
    } else {
        # Dev channel mirrors the client: recent releases, no drafts, highest semver.
        $list = Invoke-GitHubJson 'https://api.github.com/repos/risense-ai/respire-cli/releases?per_page=20'
        $release = $null
        foreach ($r in $list) {
            if ($r.draft -or -not $r.tag_name) { continue }
            $v = $r.tag_name -replace '^v', ''
            if ($v -notmatch '^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$') { continue }
            if (-not $release -or ((Compare-Semver $v ($release.tag_name -replace '^v', '')) -gt 0)) { $release = $r }
        }
        if (-not $release) { throw 'dev channel returned no usable release' }
    }

    $metaAsset = $release.assets | Where-Object { $_.name -eq "cli-build-$target.json" } | Select-Object -First 1
    if (-not $metaAsset) { throw "cli-build-$target.json not found in $($release.tag_name) assets" }
    $binaryAsset = $release.assets | Where-Object { $_.name -eq "rsrs-$target.exe" } | Select-Object -First 1
    if (-not $binaryAsset) { throw "rsrs-$target.exe not found in $($release.tag_name) assets" }

    $tempBase = if ($env:RUNNER_TEMP -and (Test-Path $env:RUNNER_TEMP)) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }
    $tmp = Join-Path $tempBase "respire-cli-fetch-$([guid]::NewGuid().ToString('N'))"
    New-Item -ItemType Directory -Path $tmp | Out-Null

    try {
        $metaPath = Join-Path $tmp 'cli-build.json'
        $binPath = Join-Path $tmp 'rsrs.exe'
        Invoke-GitHubDownload $metaAsset.browser_download_url $metaPath

        $meta = Get-Content $metaPath -Raw | ConvertFrom-Json
        if ($meta.target -ne $target) { throw "metadata target mismatch: $($meta.target) != $target" }
        if (-not $meta.version -or -not $meta.binary_file) { throw 'metadata missing version/binary_file' }
        if ($meta.binary_file -ne "rsrs-$target.exe") { throw "metadata binary_file mismatch: $($meta.binary_file)" }
        $expectedHash = $meta.binary_sha256.ToLowerInvariant()

        $outDir = Join-Path $repoRoot $OutputDir
        $cached = Join-Path $outDir 'rsrs.exe'
        if ((Test-Path $cached) -and ((Get-FileHash -LiteralPath $cached -Algorithm SHA256).Hash.ToLowerInvariant() -eq $expectedHash)) {
            Write-Output "reusing cached rsrs $($meta.version) ($target), sha256 verified"
            [IO.File]::WriteAllText((Join-Path $outDir 'version.txt'), "$($meta.version)", (New-Object Text.UTF8Encoding($false)))
            return
        }

        Invoke-GitHubDownload $binaryAsset.browser_download_url $binPath
        $actualHash = (Get-FileHash -LiteralPath $binPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $expectedHash) {
            throw "sha256 mismatch: $actualHash != $expectedHash"
        }

        $outDir = Join-Path $repoRoot $OutputDir
        New-Item -ItemType Directory -Force -Path $outDir | Out-Null
        Copy-Item -LiteralPath $binPath -Destination (Join-Path $outDir 'rsrs.exe') -Force
        # Ascii-only files for Inno Setup #define reads; versions are digits and dots.
        [IO.File]::WriteAllText((Join-Path $outDir 'version.txt'), "$($meta.version)", (New-Object Text.UTF8Encoding($false)))
        [IO.File]::WriteAllText((Join-Path $outDir 'channel.txt'), "$Channel", (New-Object Text.UTF8Encoding($false)))
        Write-Output "fetched rsrs $($meta.version) ($target, $Channel) from $($release.tag_name), sha256 verified"
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
} finally {
    Pop-Location
}

exit 0
