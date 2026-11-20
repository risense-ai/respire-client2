[CmdletBinding(DefaultParameterSetName='ExistingKeys')]
param(
    [Parameter(Mandatory,ParameterSetName='ExistingKeys')][string]$Cli,
    [Parameter(Mandatory)][string]$Client,
    [Parameter(Mandatory)][string]$RuntimeArchive,
    [Parameter(Mandatory,ParameterSetName='ExistingKeys')][string]$ModelDirectory,
    [Parameter(Mandatory,ParameterSetName='DefaultOnline')][switch]$DefaultOnline,
    [Parameter(Mandatory,ParameterSetName='DefaultOnline')][string]$PublicInputs,
    [Parameter(Mandatory,ParameterSetName='CandidateOnline')][switch]$CandidateOnline,
    [Parameter(Mandatory,ParameterSetName='CandidateOnline')][string]$CandidateInputs,
    [Parameter(Mandatory)][ValidatePattern('^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')][string]$RuntimeVersion,
    [Parameter(Mandatory)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
$online = $PSCmdlet.ParameterSetName -cne 'ExistingKeys'
$candidateMode = $PSCmdlet.ParameterSetName -ceq 'CandidateOnline'
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
$clientPath = (Resolve-Path -LiteralPath $Client).Path
$siblingCli = Join-Path (Split-Path -Parent $clientPath) 'rsrs.exe'
if (-not $online) {
    $seedCli = (Resolve-Path -LiteralPath $Cli).Path
    if ((Split-Path -Parent $seedCli) -ieq (Split-Path -Parent $clientPath) -or
        $seedCli.StartsWith(($root.TrimEnd('\') + '\'), [StringComparison]::OrdinalIgnoreCase)) {
        throw 'Require a seed CLI outside the Client2 package and fixture'
    }
}
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
$listener.Start()
$port = $listener.LocalEndpoint.Port
$listener.Stop()
$childEnvironment = @{}
foreach ($name in @('SystemRoot','WINDIR','COMSPEC','PATHEXT','ProgramFiles','ProgramFiles(x86)','ProgramData')) {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { $childEnvironment[$name] = $value }
}
$childEnvironment['PATH'] = ''
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
if (-not $online) { $childEnvironment['RSRS_M3_DIR'] = (Resolve-Path -LiteralPath $ModelDirectory).Path }
$childEnvironment['RSRS_ENGINE'] = 'cpu'
$childEnvironment['RSRS_CLIENT2_HOST'] = '1'
$childEnvironment['RSRS_NO_AUTOSYNC'] = '1'
$childEnvironment['RSRS_UPDATE_CHECK'] = '0'
$report = [ordered]@{schema=1; scope='sealed Client2 real-SDK existing-key startup diagnostic; not full GUI acceptance'; status='failed'; phase='prepare';
    core_test_mode=$false;
    runtime_version=$RuntimeVersion;
    client_sha256=(Get-FileHash $clientPath -Algorithm SHA256).Hash.ToLowerInvariant();
    child_path_empty=($childEnvironment['PATH'] -ceq '');
    initial_start=$false; normal_stop=$false; automatic_start=$false; automatic_key_handoff=$false; cleanup=$false}
if ($online) {
    $report.scope = if ($candidateMode) { 'sealed candidate default online installation diagnostic only' } else { 'latest public default online installation diagnostic only' }
    $report.diagnostic_only = $true
    $report.full_passed = $false
} else {
    $report.cli_sha256 = (Get-FileHash $seedCli -Algorithm SHA256).Hash.ToLowerInvariant()
}
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
    $start.WorkingDirectory = $root
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
    if ($Stage -ceq 'default_online_install') { $process.WaitForExit() }
    elseif (-not $process.WaitForExit(120000)) {
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
function Assert-NoManagedCLI([string]$Stage) {
    $profile = Invoke-Child $Stage $clientPath @('--headless','bootstrap-status')
    $managedCli = @(Get-ChildItem -LiteralPath $binaryDirectory -Recurse -Filter 'rsrs.exe' -File)
    if (-not $profile.ok -or -not $profile.result.installed_runtime -or $profile.result.installed_runtime_error -or
        $profile.result.cli_path -or $profile.result.cli_error -or $profile.result.payload_found -or $profile.result.payload_error -or
        $managedCli.Count -or (Test-Path -LiteralPath $siblingCli)) {
        throw 'Existing-key startup requires an installed Runtime without a managed or adjacent CLI'
    }
}
$desktop = $null
$runtimeActor = $null
try {
    if ($online) {
        $report.phase = if ($candidateMode) { 'candidate_input_binding' } else { 'public_input_binding' }
        $inputsPath = if ($candidateMode) { $CandidateInputs } else { $PublicInputs }
        $expectedScope = if ($candidateMode) { 'sealed-candidate-online-install-diagnostic' } else { 'latest-default-online-install-diagnostic' }
        $inputs = Get-Content -LiteralPath $inputsPath -Raw | ConvertFrom-Json
        if ($inputs.schema -ne 1 -or $inputs.scope -cne $expectedScope -or
            $inputs.diagnostic_only -ne $true -or $inputs.full_passed -ne $false -or
            $inputs.harness_sha -cne $env:GITHUB_SHA -or $inputs.client2.source_sha -cne $env:CANDIDATE_SOURCE -or
            $inputs.client2.version -cne $env:CANDIDATE_VERSION -or
            $inputs.runtime.version -cne $RuntimeVersion -or $inputs.client2.sha256 -cne $report.client_sha256 -or
            $inputs.runtime.archive_sha256 -cne (Get-FileHash $RuntimeArchive -Algorithm SHA256).Hash.ToLowerInvariant()) {
            throw 'Verified online inputs differ from the installation references'
        }
        if ($candidateMode) {
            $producer = $inputs.client2.producer
            if ($producer.repository -cne 'risense-ai/respire-client2' -or
                $producer.run_id -cnotmatch '^[1-9][0-9]*$' -or $producer.run_id -ne $env:INPUT_RUN_ID -or
                $producer.attempt -cnotmatch '^[1-9][0-9]*$' -or $producer.attempt -ne $env:INPUT_ATTEMPT -or
                $inputs.client2.artifact_id -cnotmatch '^[1-9][0-9]*$' -or
                $inputs.client2.artifact_zip_digest -cnotmatch '^sha256:[0-9a-f]{64}$' -or
                $inputs.client2.manifest_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
                $inputs.client2.six_package_receipts_verified -ne $true -or
                $inputs.runtime.run_id -cnotmatch '^[1-9][0-9]*$' -or $inputs.runtime.run_id -ne $env:RUNTIME_RUN_ID) {
                throw 'Sealed candidate or Runtime producer proof differs'
            }
            $report.candidate_inputs = $inputs
        } else {
            $report.public_inputs = $inputs
        }
        $fresh = Invoke-Child 'fresh_profile' $clientPath @('--headless','bootstrap-status')
        if (-not $fresh.ok -or $fresh.result.payload_found -or $fresh.result.payload_error -or $fresh.result.cli_path -or
            $fresh.result.cli_error -or $fresh.result.installed_runtime -or $fresh.result.installed_runtime_error -or
            (Test-Path -LiteralPath $cliPath) -or (Test-Path -LiteralPath $siblingCli) -or
            (Test-Path -LiteralPath (Join-Path $binaryDirectory 'runtime.json'))) {
            throw 'Default online install requires absent CLI, Runtime and bootstrap payload'
        }
        $report.cli_absent_before = $true
        $report.runtime_absent_before = $true
        # Keep factory channel selection; suppress only post-install startup for this installation-only scope.
        [IO.File]::WriteAllText((Join-Path $dataDirectory 'client2/preferences.json'), '{"runtimeAutoRestart":false}', [Text.UTF8Encoding]::new($false))
        $report.install_restart_disabled = $true
        $installed = Invoke-Child 'default_online_install' $clientPath @('--headless','install-runtime')
        if (-not $installed.ok) { throw 'Default online installation failed' }
        $selection = Get-Content -LiteralPath (Join-Path $binaryDirectory 'runtime.json') -Raw | ConvertFrom-Json
        $managed = Join-Path $binaryDirectory "runtime/$RuntimeVersion"
        $installedRuntimeHash = (Get-FileHash (Join-Path $managed 'rsrs-runtime.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($selection.version -cne $RuntimeVersion -or $selection.sha256 -cne $inputs.runtime.binary_sha256 -or
            $installedRuntimeHash -cne $selection.sha256) {
            throw 'Installed Runtime differs from the verified public bytes'
        }
        if ((Get-FileHash $inputs.runtime_metadata_path -Algorithm SHA256).Hash.ToLowerInvariant() -cne $inputs.runtime_metadata_sha256) {
            throw 'Verified public Runtime metadata changed before installed payload checks'
        }
        $metadata = Get-Content -LiteralPath $inputs.runtime_metadata_path -Raw | ConvertFrom-Json
        foreach ($file in @($metadata.files | Where-Object { -not $_.path.StartsWith('core-notices/', [StringComparison]::Ordinal) })) {
            # The installed tree uses the host binary name instead of the release asset name.
            $installedName = if ($file.path -ceq $metadata.binary_file) { 'rsrs-runtime.exe' } else { $file.path }
            $installedFile = Join-Path $managed $installedName
            if ((Get-Item -LiteralPath $installedFile).Length -ne $file.size -or
                (Get-FileHash $installedFile -Algorithm SHA256).Hash.ToLowerInvariant() -cne $file.sha256) {
                throw 'Installed Runtime payload differs from the verified public inventory'
            }
        }
        $after = Invoke-Child 'runtime_only_profile' $clientPath @('--headless','bootstrap-status')
        if (-not $after.ok -or $after.result.cli_path -or $after.result.cli_error -or
            $after.result.payload_found -or $after.result.payload_error -or (Test-Path -LiteralPath $cliPath) -or
            (Test-Path -LiteralPath $siblingCli) -or
            $after.result.installed_runtime_error -or $after.result.installed_runtime.version -cne $selection.version -or
            $after.result.installed_runtime.sha256 -cne $installedRuntimeHash) {
            throw 'Online installation did not leave a verified Runtime-only profile'
        }
        $preferences = Get-Content -LiteralPath (Join-Path $dataDirectory 'client2/preferences.json') -Raw | ConvertFrom-Json
        if ($preferences.PSObject.Properties.Name -contains 'runtimeChannel') { throw 'Default channel was overridden' }
        $report.cli_absent_after = $true
        $report.installed_runtime_version = $selection.version
        $report.installed_runtime_sha256 = $installedRuntimeHash
        $report.installed_runtime_payload_verified = $true
        $report.factory_channel_retained = $true
        if ($candidateMode) { $report.candidate_online_install = $true } else { $report.default_online_install = $true }
        $report.status = 'diagnostic-passed'
    } else {
    $installed = Invoke-Child 'install' $seedCli @('--json','runtime','install','--file',$RuntimeArchive,'--version',$RuntimeVersion)
    if ($installed.status -cne 'ok') { throw 'Runtime install did not succeed' }
    $preferences = '{"autoRuntime":false,"autoUpdate":false,"clientAutoUpdate":false,"runtimeAutoUpdate":false,"runtimeAutoRestart":false,"onboardingVersion":1,"onboardingCompleted":true,"onboardingStep":"done","onboardingSyncChoice":"offline"}'
    $preferencesPath = Join-Path $dataDirectory 'client2/preferences.json'
    [IO.File]::WriteAllText($preferencesPath, $preferences, [Text.UTF8Encoding]::new($false))
    $keys = Invoke-Child 'keygen' $seedCli @('--json','keygen','--pass',('Synthetic-' + [Guid]::NewGuid().ToString('N')))
    if ($keys.status -cne 'ok' -or -not $keys.summary.super) { throw 'Synthetic offline keygen did not succeed' }
    $keys = $null
    $seedStart = Invoke-Child 'seed_start' $seedCli @('--json','runtime','start')
    if ($seedStart.status -cne 'ok') { throw 'Seed Runtime did not start' }
    $seedHealth = Health
    $runtimeActor = Assert-Actor $seedHealth
    if (-not $seedHealth.account_id -or -not $seedHealth.store_ready) { throw 'Seed library was not ready' }
    $text = 'published-startup-' + [Guid]::NewGuid().ToString('N')
    $saved = Invoke-Child 'remember' $seedCli @('--client-only','--json','remember',$text,'--title',$text,'--importance','important','--force')
    if ($saved.status -cne 'ok' -or -not $saved.summary.id) { throw 'Synthetic memory write did not succeed' }
    $memoryId = $saved.summary.id
    $seedStop = Invoke-Child 'seed_stop' $seedCli @('--json','runtime','stop')
    if ($seedStop.status -cne 'ok' -or (Get-CimInstance Win32_Process -Filter "ProcessId=$($runtimeActor.ProcessId)")) { throw 'Seed Runtime did not stop' }
    $runtimeActor = $null
    Assert-NoManagedCLI 'original_library_without_cli'
    $report.cli_absent_before_start = $true
    $started = Invoke-Child 'initial_start' $clientPath @('--headless','start')
    if (-not $started.ok) { throw 'Initial Client2 start did not succeed' }
    $runtimeActor = Assert-Actor (Health)
    $initialHealth = Health
    if ($initialHealth.store_ready -ne $true -or $initialHealth.credential_state -cne 'available' -or
        $initialHealth.account_id -cne $seedHealth.account_id -or $initialHealth.pid -eq $seedHealth.pid) { throw 'Initial key handoff differs' }
    Assert-NoManagedCLI 'first_started_library_without_cli'
    $previousId = $runtimeActor.ProcessId
    $report.initial_start = $true
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
    $report.health_probe_timeouts = 0
    do {
        try { $health = Health } catch [Net.Http.HttpRequestException] { $health = $null }
        catch [Threading.Tasks.TaskCanceledException] {
            if ($_.Exception.InnerException -isnot [TimeoutException]) { throw }
            $report.health_probe_timeouts++
            $health = $null
        }
        if ($health) {
            $report.runtime_observations += @{pid=$health.pid; store_ready=$health.store_ready; credential_state=$(if ($health.credential_state -cin @('required','available')) { $health.credential_state } else { 'other' })}
            $runtimeActor = Assert-Actor $health
            break
        }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before automatic startup' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if (-not $runtimeActor -or $runtimeActor.ProcessId -eq $previousId -or
        $runtimeActor.ParentProcessId -ne $desktop.Id) { throw 'Automatic startup did not yield a new actor' }
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
    # Wait for native startup to finish before the first business request.
    $report.phase = 'host_start_completion'
    $markerPath = Join-Path $dataDirectory 'runtime/autostart/maintenance.json'
    $report.maintenance_observed = $false
    do {
        $health = Health
        $markerPresent = Test-Path -LiteralPath $markerPath
        if ($markerPresent) { $report.maintenance_observed = $true }
        if ($health.startup_complete -eq $true -and -not $markerPresent) { break }
        if ($desktop.WaitForExit(500)) { throw 'Desktop exited before host startup completion' }
    } while ($deadline.Elapsed.TotalSeconds -lt 60)
    if ($health.startup_complete -ne $true -or $markerPresent) { throw 'Host startup maintenance did not complete' }
    $report.host_start_completed = $true
    Assert-NoManagedCLI 'restarted_library_without_cli'
    $report.cli_absent_after_restart = $true
    $report.phase = 'automatic_key_handoff'
    $health = Health
    $publicStates = @('ready','locked','running','draining','stopping','stopped','error','maintenance_requested','resources_closing')
    $report.health_state = if ($health.state -cin $publicStates) { $health.state } else { 'other' }
    $report.control_state = if ($health.control.state -cin $publicStates) { $health.control.state } else { 'other' }
    $report.stop_operation_present = -not [string]::IsNullOrEmpty($health.control.operation_id)
    $report.installation_root_matches = [string]::Equals($health.installation_root, $dataDirectory, [StringComparison]::OrdinalIgnoreCase)
    $report.account_root_matches = [string]::Equals($health.data_dir, $dataDirectory, [StringComparison]::OrdinalIgnoreCase)
    if ($health.pid -ne $startedHealth.pid -or $health.instance_id -cne $startedHealth.instance_id -or $health.generation -ne $startedHealth.generation) { throw 'Runtime identity changed during key handoff' }
    if ($health.v -ne 1 -or $health.account_id -cne $seedHealth.account_id -or -not $health.instance_id -or $health.generation -le 0 -or
        -not $report.installation_root_matches -or $health.data_dir -ine $seedHealth.data_dir) { throw 'Runtime RPC scope differs from the seed library' }
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
    $report.direct_rpc_readback = $true
    $legacyRead = Invoke-Child 'legacy_cli_readback' $seedCli @('--client-only','--json','show',$memoryId)
    if ($legacyRead.status -cne 'ok' -or $legacyRead.details.entry.id -cne $memoryId -or
        $legacyRead.details.entry.content -cne $text) { throw 'Legacy CLI readback differs' }
    Assert-NoManagedCLI 'reconciled_library_without_cli'
    $report.legacy_cli_readback = $true
    $report.status = 'diagnostic-passed'
    }
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
        'Desktop exited before host startup completion'='desktop_exited_before_host_completion'
        'Host startup maintenance did not complete'='host_start_maintenance_timeout'
        'Runtime RPC scope differs from the seed library'='rpc_scope_mismatch'
        'Existing-key startup requires an installed Runtime without a managed or adjacent CLI'='unexpected_cli_or_runtime'
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
