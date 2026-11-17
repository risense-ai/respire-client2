param(
    [Parameter(Mandatory)][string]$Cli,
    [Parameter(Mandatory)][string]$Client,
    [Parameter(Mandatory)][string]$RuntimeArchive,
    [Parameter(Mandatory)][string]$ModelDirectory,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_ENVIRONMENT -cne 'github-hosted' -or -not $IsWindows) {
    throw 'This diagnostic requires a fresh hosted Windows runner'
}
$root = [IO.Path]::GetFullPath($OutputDirectory)
$temporary = [IO.Path]::GetFullPath($env:RUNNER_TEMP).TrimEnd('\') + '\'
if (-not $root.StartsWith($temporary, [StringComparison]::OrdinalIgnoreCase) -or (Test-Path -LiteralPath $root)) {
    throw 'Require a new diagnostic directory inside RUNNER_TEMP'
}
$homeDirectory = Join-Path $root 'home'
$dataDirectory = Join-Path $homeDirectory '.rsrs'
$binaryDirectory = Join-Path $dataDirectory 'bin'
foreach ($dir in @($binaryDirectory, (Join-Path $dataDirectory 'client2'), (Join-Path $root 'appdata'), (Join-Path $root 'local'), (Join-Path $root 'temp'))) {
    New-Item -ItemType Directory -Path $dir | Out-Null
}
$cliPath = Join-Path $binaryDirectory 'rsrs.exe'
Copy-Item -LiteralPath $Cli -Destination $cliPath
$clientPath = (Resolve-Path -LiteralPath $Client).Path
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = $listener.LocalEndpoint.Port
$listener.Stop()
$childEnvironment = @{}
foreach ($name in @('SystemRoot','WINDIR','COMSPEC','PATHEXT','ProgramFiles','ProgramFiles(x86)','ProgramData')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { $childEnvironment[$name] = $value }
}
$childEnvironment['PATH'] = "$env:SystemRoot\System32;$env:SystemRoot"
$childEnvironment['HOME'] = $homeDirectory
$childEnvironment['USERPROFILE'] = $homeDirectory
$childEnvironment['APPDATA'] = Join-Path $root 'appdata'
$childEnvironment['LOCALAPPDATA'] = Join-Path $root 'local'
$childEnvironment['TEMP'] = Join-Path $root 'temp'
$childEnvironment['TMP'] = Join-Path $root 'temp'
$childEnvironment['RSRS_DATA_DIR'] = $dataDirectory
$childEnvironment['RSRS_BIN_DIR'] = $binaryDirectory
$childEnvironment['RSRS_RPC_PORT'] = "$port"
$childEnvironment['RSRS_INSTANCE_ID'] = 'published-startup-' + [Guid]::NewGuid().ToString('N')
$childEnvironment['RSRS_M3_DIR'] = (Resolve-Path -LiteralPath $ModelDirectory).Path
$childEnvironment['RSRS_ENGINE'] = 'cpu'
$childEnvironment['RSRS_CLIENT2_HOST'] = '1'
$childEnvironment['RSRS_CORE_TEST_MODE'] = '1'
$childEnvironment['RSRS_NO_AUTOSYNC'] = '1'
$childEnvironment['RSRS_UPDATE_CHECK'] = '0'
$report = [ordered]@{schema=1; scope='published Client2 existing-key startup diagnostic; not full GUI acceptance'; status='failed'; phase='prepare';
    runtime_version='2.0.0-dev.23'; cli_sha256=(Get-FileHash $cliPath -Algorithm SHA256).Hash.ToLowerInvariant();
    client_sha256=(Get-FileHash $clientPath -Algorithm SHA256).Hash.ToLowerInvariant();
    initial_start=$false; normal_stop=$false; automatic_start=$false; automatic_key_handoff=$false; cleanup=$false}
$categories = [ordered]@{
    connection_refused='积极拒绝|actively refused|connection refused'
    health_unconfirmed='Runtime 状态无法确认'
    installation_proof='安装凭据|身份或版本不匹配|release.*(hash|metadata)|checksum'
    manual_stop='manually stopped|startup is disabled'
    keygen_pending='offline_keygen_handoff_pending'
    credential_backend='credential backend|keyring|Credential Manager|credential.*unavailable'
    identity_changed='identity.*(changed|mismatch)|owner.*(changed|unverifiable)|recorded owner without verifiable health'
    receipt_invalid='未返回完整回执|未返回有效 JSON|回执协议|startup.*uncertain'
    runtime_unavailable='runtime_not_running|Runtime is not running|runtime is not running|Runtime 未运行|no running runtime'
    rpc_scope='scope|generation|instance_id'
    command_input='invalid_input|unrecognized|unexpected argument'
    memory_missing='not_found|not found|不存在'
    timeout='timed out|deadline|超时'
}
function Classify([string]$Text) {
    foreach ($entry in $categories.GetEnumerator()) { if ($Text -match $entry.Value) { return $entry.Key } }
    return 'unclassified'
}
function Start-Child([string]$Executable, [string[]]$Arguments) {
    $start = [Diagnostics.ProcessStartInfo]::new($Executable)
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.Environment.Clear()
    foreach ($key in $childEnvironment.Keys) { $start.Environment[$key] = $childEnvironment[$key] }
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    return [Diagnostics.Process]::Start($start)
}
function Invoke-Child([string]$Stage, [string]$Executable, [string[]]$Arguments) {
    $report.phase = $Stage
    $process = Start-Child $Executable $Arguments
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(120000)) {
        $process.Kill($true)
        $process.WaitForExit()
        throw 'Child operation timed out'
    }
    $output = $stdout.GetAwaiter().GetResult()
    $errorOutput = $stderr.GetAwaiter().GetResult()
    if ($process.ExitCode -ne 0) {
        $report.child_exit = $process.ExitCode
        $report.failure_category = Classify ($output + $errorOutput)
        try {
            $failure = $output | ConvertFrom-Json
            if ($failure.summary.reason -cin @('runtime_error','invalid_input','not_found','rpc_error','client_only')) {
                $report.child_reason = $failure.summary.reason
            }
            if ($failure.details.error_type -cin @('runtime','input','rpc','validation')) {
                $report.child_error_type = $failure.details.error_type
            }
        } catch { $report.child_json = $false }
        throw 'Child operation failed; raw output withheld'
    }
    return ($output | ConvertFrom-Json)
}
function Health {
    return Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/health" -Headers @{'X-Respire-Health'='runtime-v1'} -TimeoutSec 3 -NoProxy
}
function Assert-Actor($Health) {
    $expected = Join-Path $binaryDirectory 'runtime/2.0.0-dev.23/rsrs-runtime.exe'
    $actor = Get-CimInstance Win32_Process -Filter "ProcessId=$($Health.pid)"
    if (-not $actor -or -not [string]::Equals($actor.ExecutablePath, $expected, [StringComparison]::OrdinalIgnoreCase) -or
        $Health.store_ready -ne $true -or $Health.credential_state -cne 'available') { throw 'Runtime actor or key handoff differs' }
    return $actor
}
$desktop = $null
$runtimeActor = $null
try {
    $installed = Invoke-Child 'install' $cliPath @('--json','runtime','install','--file',$RuntimeArchive,'--version','2.0.0-dev.23')
    if ($installed.status -cne 'ok') { throw 'Runtime install did not succeed' }
    $preferences = '{"autoRuntime":false,"autoUpdate":false,"clientAutoUpdate":false,"runtimeAutoUpdate":false,"runtimeAutoRestart":false,"onboardingVersion":1,"onboardingCompleted":true,"onboardingStep":"done","onboardingSyncChoice":"offline"}'
    $preferencesPath = Join-Path $dataDirectory 'client2/preferences.json'
    [IO.File]::WriteAllText($preferencesPath, $preferences, [Text.UTF8Encoding]::new($false))
    $keys = Invoke-Child 'keygen' $cliPath @('--json','keygen','--pass',('Synthetic-' + [Guid]::NewGuid().ToString('N')))
    if ($keys.status -cne 'ok' -or -not $keys.summary.super) { throw 'Synthetic offline keygen did not succeed' }
    $keys = $null
    $started = Invoke-Child 'initial_start' $clientPath @('--headless','start')
    if (-not $started.ok) { throw 'Initial Client2 start did not succeed' }
    $runtimeActor = Assert-Actor (Health)
    $previousId = $runtimeActor.ProcessId
    $report.initial_start = $true
    $text = 'published-startup-' + [Guid]::NewGuid().ToString('N')
    $saved = Invoke-Child 'remember' $cliPath @('--client-only','--json','remember',$text,'--title',$text,'--importance','important','--force')
    if ($saved.status -cne 'ok' -or -not $saved.summary.id) { throw 'Synthetic memory write did not succeed' }
    $memoryId = $saved.summary.id
    $stopped = Invoke-Child 'normal_stop' $clientPath @('--headless','stop')
    if (-not $stopped.ok -or (Get-CimInstance Win32_Process -Filter "ProcessId=$previousId")) { throw 'Normal Stop did not remove the initial actor' }
    $runtimeActor = $null
    $report.normal_stop = $true
    $preferences = $preferences | ConvertFrom-Json
    $preferences.autoRuntime = $true
    [IO.File]::WriteAllText($preferencesPath, ($preferences | ConvertTo-Json -Compress), [Text.UTF8Encoding]::new($false))
    $report.phase = 'existing_keys_gui_restart'
    $desktop = Start-Child $clientPath @('--tray')
    $deadline = [Diagnostics.Stopwatch]::StartNew()
    do {
        try { $health = Health } catch [Net.Http.HttpRequestException] { $health = $null }
        if ($health) { $runtimeActor = Assert-Actor $health; break }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before automatic startup' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if (-not $runtimeActor -or $runtimeActor.ProcessId -eq $previousId) { throw 'Automatic startup did not yield a new actor' }
    $report.automatic_start = $true
    $report.runtime_pid = $runtimeActor.ProcessId
    # Use the original native GUI fixture's scoped read-only RPC oracle.
    $report.phase = 'automatic_key_handoff'
    $health = Health
    if ($health.v -ne 1 -or -not $health.account_id -or -not $health.instance_id -or $health.generation -le 0) { throw 'Runtime RPC scope missing' }
    $scope = @{account_id=$health.account_id; instance_id=$health.instance_id; generation=$health.generation}
    $requestId = 'published-startup-' + [Guid]::NewGuid().ToString('N')
    $request = @{v=1; id=$requestId; operation_id=$requestId; idempotency_key=$requestId; scope=$scope; method='cli.exec'; args=@('--json','show',$memoryId)}
    $read = Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/rpc" -Method Post -ContentType 'application/json' -Body ($request | ConvertTo-Json -Depth 5 -Compress) -TimeoutSec 30 -NoProxy
    if ($read.v -ne 1 -or $read.id -cne $requestId -or -not $read.ok -or $read.exit -ne 0 -or
        $read.operation_id -cne $requestId -or $read.idempotency_key -cne $requestId -or
        $read.scope.account_id -cne $scope.account_id -or $read.scope.instance_id -cne $scope.instance_id -or $read.scope.generation -ne $scope.generation -or
        $read.envelope.command -cne 'show' -or $read.envelope.status -cne 'ok' -or @($read.envelope.errors).Count -ne 0 -or
        $read.envelope.details.entry.id -cne $memoryId -or $read.envelope.details.entry.content -cne $text) { throw 'Automatic key handoff did not reopen the original memory' }
    $afterRead = Health
    if ($afterRead.pid -ne $health.pid -or $afterRead.instance_id -cne $health.instance_id -or $afterRead.generation -ne $health.generation) { throw 'Runtime identity changed during readback' }
    $report.automatic_key_handoff = $true
    $legacyRead = Invoke-Child 'legacy_cli_readback' $cliPath @('--client-only','--json','show',$memoryId)
    if ($legacyRead.status -cne 'ok' -or $legacyRead.details.entry.content -cne $text) { throw 'Legacy CLI readback differs' }
    $report.legacy_cli_readback = $true
    $report.status = 'diagnostic-passed'
} catch {
    $report.failure_phase = $report.phase
    if (-not $report.failure_category) { $report.failure_category = Classify $_.Exception.Message }
} finally {
    $logPath = Join-Path $dataDirectory 'client2/client.log'
    if (Test-Path -LiteralPath $logPath) {
        $lines = @(Get-Content -LiteralPath $logPath | Where-Object { $_ -match '连接 Runtime:' })
        $report.startup_errors = @($lines | ForEach-Object { Classify $_ })
    }
    try {
        if ($runtimeActor) {
            $current = Get-CimInstance Win32_Process -Filter "ProcessId=$($runtimeActor.ProcessId)"
            if ($current -and $current.CreationDate -eq $runtimeActor.CreationDate -and $current.ExecutablePath -ceq $runtimeActor.ExecutablePath) {
                $null = Invoke-Child 'cleanup_stop' $clientPath @('--headless','stop')
            }
        }
        if ($desktop -and -not $desktop.HasExited) { $desktop.Kill($true); $desktop.WaitForExit() }
        $expectedRuntime = Join-Path $binaryDirectory 'runtime/2.0.0-dev.23/rsrs-runtime.exe'
        $remaining = @(Get-CimInstance Win32_Process -Filter "name='rsrs-runtime.exe'" | Where-Object {
            [string]::Equals($_.ExecutablePath, $expectedRuntime, [StringComparison]::OrdinalIgnoreCase)
        })
        if ($remaining.Count) { throw 'An isolated Runtime remains; cleanup is unconfirmed' }
        $report.cleanup = $true
    } catch { $report.cleanup_category = Classify $_.Exception.Message; $report.status = 'failed' }
    [IO.File]::WriteAllText((Join-Path $root 'startup-diagnostic.json'), ($report | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
}
if ($report.status -cne 'diagnostic-passed') { exit 1 }
