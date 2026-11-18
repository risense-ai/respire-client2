param(
    [Parameter(Mandatory)][string]$Cli,
    [Parameter(Mandatory)][string]$Client,
    [Parameter(Mandatory)][string]$RuntimeArchive,
    [Parameter(Mandatory)][string]$ModelDirectory,
    [ValidateSet('2.0.0-dev.24')][string]$RuntimeVersion = '2.0.0-dev.24',
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -cne 'true' -or $env:RUNNER_ENVIRONMENT -cne 'github-hosted' -or -not $IsWindows) {
    throw 'This diagnostic requires a fresh hosted Windows runner'
}
if (Test-Path Env:RSRS_CORE_TEST_MODE) { throw 'Core test mode is forbidden for real startup acceptance' }
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
$childEnvironment['RSRS_NO_AUTOSYNC'] = '1'
$childEnvironment['RSRS_UPDATE_CHECK'] = '0'
$report = [ordered]@{schema=1; scope='sealed Client2 real-SDK existing-key startup diagnostic; not full GUI acceptance'; status='failed'; phase='prepare';
    core_test_mode=$false;
    runtime_version=$RuntimeVersion; cli_sha256=(Get-FileHash $cliPath -Algorithm SHA256).Hash.ToLowerInvariant();
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
    invalid_config='invalid_config|client config|configuration.*changed'
    runtime_locked='runtime_locked|supply private key material|library remains locked'
    runtime_maintenance='runtime_maintenance|runtime (maintenance|is draining)'
    runtime_worker='runtime worker is gone|dispatcher.*disconnected'
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
    $expected = Join-Path $binaryDirectory "runtime/$RuntimeVersion/rsrs-runtime.exe"
    $actor = Get-CimInstance Win32_Process -Filter "ProcessId=$($Health.pid)"
    $report.actor_found = $null -ne $actor
    $report.actor_path_match = [string]::Equals($actor.ExecutablePath, $expected, [StringComparison]::OrdinalIgnoreCase)
    if (-not $actor -or -not [string]::Equals($actor.ExecutablePath, $expected, [StringComparison]::OrdinalIgnoreCase)) { throw 'Runtime actor identity differs' }
    return $actor
}
$desktop = $null
$runtimeActor = $null
try {
    $installed = Invoke-Child 'install' $cliPath @('--json','runtime','install','--file',$RuntimeArchive,'--version',$RuntimeVersion)
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
    $initialHealth = Health
    if ($initialHealth.store_ready -ne $true -or $initialHealth.credential_state -cne 'available') { throw 'Initial key handoff differs' }
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
    $report.runtime_observations = @()
    do {
        try { $health = Health } catch [Net.Http.HttpRequestException] { $health = $null }
        if ($health) {
            $report.runtime_observations += @{pid=$health.pid; store_ready=$health.store_ready; credential_state=$(if ($health.credential_state -cin @('required','available')) { $health.credential_state } else { 'other' })}
            $runtimeActor = Assert-Actor $health
            break
        }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before automatic startup' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if (-not $runtimeActor -or $runtimeActor.ProcessId -eq $previousId) { throw 'Automatic startup did not yield a new actor' }
    $report.automatic_start = $true
    $report.runtime_pid = $runtimeActor.ProcessId
    # Use the original native GUI fixture's scoped read-only RPC oracle.
    $report.phase = 'automatic_key_handoff'
    $startedHealth = $health
    do {
        $health = Health
        if ($health.pid -ne $startedHealth.pid -or $health.instance_id -cne $startedHealth.instance_id -or $health.generation -ne $startedHealth.generation) { throw 'Runtime identity changed during key handoff' }
        if ($health.store_ready -eq $true -and $health.credential_state -ceq 'available') { break }
        if ($health.store_ready -ne $false -or $health.credential_state -cne 'required') { throw 'Runtime key state is not a pending handoff' }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before key handoff' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if ($health.store_ready -ne $true -or $health.credential_state -cne 'available') { throw 'Automatic key handoff timed out' }
    # Health/credential readiness precedes the host's maintenance completion.
    # Observe that controller; never delete its marker or retry rejected business.
    $report.phase = 'host_start_completion'
    $markerPath = Join-Path $dataDirectory 'runtime/autostart/maintenance.json'
    $report.host_start_observed = $false
    $report.maintenance_observed = $false
    do {
        $controllers = @(Get-CimInstance Win32_Process -Filter "ParentProcessId=$($desktop.Id)" | Where-Object {
            [string]::Equals($_.ExecutablePath, $cliPath, [StringComparison]::OrdinalIgnoreCase) -and
            $_.CommandLine -match '(?:^|\s)runtime\s+start(?:\s|$)'
        })
        $markerPresent = Test-Path -LiteralPath $markerPath
        if ($controllers.Count) { $report.host_start_observed = $true }
        if ($markerPresent) {
            $report.maintenance_observed = $true
            try { $marker = [IO.File]::ReadAllText($markerPath) | ConvertFrom-Json }
            catch [IO.FileNotFoundException] { $markerPresent = $false; $report.marker_removed_during_observation = $true }
            if ($markerPresent) {
                $report.maintenance_phase = if ($marker.phase -cin @('prepared','paused','starting','verified')) { $marker.phase } else { 'other' }
                $owner = @($controllers | Where-Object ProcessId -EQ $marker.owner_pid)
                $report.maintenance_owner_is_startup_cli = $owner.Count -eq 1
                if ($owner.Count -ne 1) { throw 'Maintenance marker does not belong to a live startup CLI' }
            }
        }
        if (-not $controllers.Count -and -not $markerPresent) { break }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before host startup completion' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if ($controllers.Count -or $markerPresent) { throw 'Host startup maintenance did not complete' }
    $report.host_start_completed = $true
    $report.phase = 'automatic_key_handoff'
    $health = Health
    $publicStates = @('ready','locked','running','draining','stopping','stopped','error','maintenance_requested','resources_closing')
    $report.health_state = if ($health.state -cin $publicStates) { $health.state } else { 'other' }
    $report.control_state = if ($health.control.state -cin $publicStates) { $health.control.state } else { 'other' }
    $report.stop_operation_present = -not [string]::IsNullOrEmpty($health.control.operation_id)
    $report.installation_root_matches = [string]::Equals($health.installation_root, $dataDirectory, [StringComparison]::OrdinalIgnoreCase)
    $report.account_root_matches = [string]::Equals($health.data_dir, $dataDirectory, [StringComparison]::OrdinalIgnoreCase)
    if ($health.pid -ne $startedHealth.pid -or $health.instance_id -cne $startedHealth.instance_id -or $health.generation -ne $startedHealth.generation) { throw 'Runtime identity changed during key handoff' }
    if ($health.v -ne 1 -or -not $health.account_id -or -not $health.instance_id -or $health.generation -le 0) { throw 'Runtime RPC scope missing' }
    $scope = @{account_id=$health.account_id; instance_id=$health.instance_id; generation=$health.generation}
    $requestId = 'published-startup-' + [Guid]::NewGuid().ToString('N')
    $request = @{v=1; id=$requestId; operation_id=$requestId; idempotency_key=$requestId; scope=$scope; method='cli.exec'; args=@('--json','show',$memoryId)}
    $read = Invoke-RestMethod -Uri "http://127.0.0.1:$port/api/rpc" -Method Post -ContentType 'application/json' -Body ($request | ConvertTo-Json -Depth 5 -Compress) -TimeoutSec 30 -NoProxy
    $report.rpc_checks = [ordered]@{
        protocol=($read.v -eq 1); request_id=($read.id -ceq $requestId); ok=($read.ok -eq $true); exit_zero=($read.exit -eq 0)
        operation_id=($read.operation_id -ceq $requestId); idempotency_key=($read.idempotency_key -ceq $requestId)
        account_scope=($read.scope.account_id -ceq $scope.account_id); instance_scope=($read.scope.instance_id -ceq $scope.instance_id)
        generation_scope=($read.scope.generation -eq $scope.generation); command=($read.envelope.command -ceq 'show')
        status=($read.envelope.status -ceq 'ok'); errors_empty=(@($read.envelope.errors).Count -eq 0)
        memory_id=($read.envelope.details.entry.id -ceq $memoryId); memory_content=($read.envelope.details.entry.content -ceq $text)
    }
    $report.rpc_error_category = Classify ($read.error + ($read.envelope.errors -join ' '))
    $rpcCodes = @('invalid_config','runtime_locked','scope_mismatch','scope_required','daemon_unavailable','runtime_maintenance',
        'model_switch_busy','request_in_progress','operation_id_conflict','invalid_operation_id','invalid_request_id','protocol_version_mismatch',
        'unknown_method','queue_full','request_timeout')
    $report.rpc_code = if ($read.code -cin $rpcCodes) { $read.code } elseif ($null -eq $read.code) { 'none' } else { 'other' }
    $report.rpc_envelope_present = $null -ne $read.envelope
    $fixedRpcErrors = @('runtime maintenance rejected business execution', 'runtime maintenance pauses model/index preparation',
        'runtime maintenance rejected queued business', 'runtime maintenance rejected queued execution',
        'runtime is draining and rejected new business', 'runtime_maintenance: runtime maintenance rejected queued business')
    $report.rpc_error = if ($read.error -cin $fixedRpcErrors) { $read.error } else { 'other' }
    $report.maintenance_after_read = Test-Path -LiteralPath $markerPath
    $afterRejection = Health
    $report.control_state_after_read = if ($afterRejection.control.state -cin $publicStates) { $afterRejection.control.state } else { 'other' }
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
    # Preserve the first error without retaining exception text, response bodies or keys.
    $report.failure_exception_type = $_.Exception.GetType().FullName
    $report.failure_inner_exception_type = if ($_.Exception.InnerException) { $_.Exception.InnerException.GetType().FullName } else { $null }
    $report.failure_script_line = $_.InvocationInfo.ScriptLineNumber
    $report.failure_command = $_.InvocationInfo.MyCommand.Name
    $report.failure_http_status = if ($_.Exception.Response) { [int]$_.Exception.Response.StatusCode } else { $null }
    $fixedFailures = @{
        'Runtime actor identity differs'='runtime_actor_identity_mismatch'
        'Initial key handoff differs'='initial_key_handoff_failed'
        'Automatic startup did not yield a new actor'='automatic_start_not_observed'
        'Desktop exited before automatic startup'='desktop_exited_before_start'
        'Runtime identity changed during key handoff'='runtime_changed_during_handoff'
        'Runtime key state is not a pending handoff'='unexpected_credential_state'
        'Desktop exited before key handoff'='desktop_exited_before_handoff'
        'Automatic key handoff timed out'='automatic_key_handoff_timeout'
        'Maintenance marker does not belong to a live startup CLI'='startup_maintenance_owner_unproven'
        'Desktop exited before host startup completion'='desktop_exited_before_host_completion'
        'Host startup maintenance did not complete'='host_start_maintenance_timeout'
        'Runtime RPC scope missing'='rpc_scope_missing'
        'Automatic key handoff did not reopen the original memory'='original_memory_rpc_mismatch'
        'Runtime identity changed during readback'='runtime_changed_during_readback'
        'Child operation failed; raw output withheld'='child_operation_failed'
        'Legacy CLI readback differs'='legacy_cli_readback_mismatch'
    }
    $report.failure_code = if ($fixedFailures.ContainsKey($_.Exception.Message)) { $fixedFailures[$_.Exception.Message] } else { 'diagnostic_operation_failed' }
    if (-not $report.failure_category) { $report.failure_category = Classify $_.Exception.Message }
} finally {
    $logPath = Join-Path $dataDirectory 'client2/client.log'
    if (Test-Path -LiteralPath $logPath) {
        $report.client_log_present = $true
        $report.client_log_bytes = (Get-Item -LiteralPath $logPath).Length
        $allLogLines = @(Get-Content -LiteralPath $logPath)
        $publicLogLines = @($allLogLines | Where-Object { $_ -cmatch '^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} \[WebView2\] Environment created successfully$' })
        $report.client_log_omitted_lines = $allLogLines.Count - $publicLogLines.Count
        [IO.File]::WriteAllLines((Join-Path $root 'client.log'), [string[]]$publicLogLines, [Text.UTF8Encoding]::new($false))
        $lines = @($allLogLines | Where-Object { $_ -match '连接 Runtime:' })
        $report.startup_error_count = $lines.Count
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
        $expectedRuntime = Join-Path $binaryDirectory "runtime/$RuntimeVersion/rsrs-runtime.exe"
        $remaining = @(Get-CimInstance Win32_Process -Filter "name='rsrs-runtime.exe'" | Where-Object {
            [string]::Equals($_.ExecutablePath, $expectedRuntime, [StringComparison]::OrdinalIgnoreCase)
        })
        if ($remaining.Count) { throw 'An isolated Runtime remains; cleanup is unconfirmed' }
        $report.cleanup = $true
    } catch { $report.cleanup_category = Classify $_.Exception.Message; $report.status = 'failed' }
    [IO.File]::WriteAllText((Join-Path $root 'startup-diagnostic.json'), ($report | ConvertTo-Json -Depth 6), [Text.UTF8Encoding]::new($false))
}
if ($report.status -cne 'diagnostic-passed') { exit 1 }
