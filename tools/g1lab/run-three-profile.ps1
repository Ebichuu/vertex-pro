[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('I_CONFIRM_EMPTY_ISOLATED_LAB')]
    [string]$Confirmation,
    [switch]$SkipBuild,
    [switch]$KeepLab,
    [ValidateRange(1024, 65535)]
    [int]$Port = 17481
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$compose = Join-Path $root 'deploy/compose/lab/g1-compose.yaml'
$evidenceRoot = Join-Path $root 'docs/evidence/qui-12'
$previousEnvironment = @{
    VERTEX_G1_QUI_PORT = $env:VERTEX_G1_QUI_PORT
    VERTEX_PRO_QUI_URL = $env:VERTEX_PRO_QUI_URL
    VERTEX_PRO_QUI_PROXY_KEY = $env:VERTEX_PRO_QUI_PROXY_KEY
    VERTEX_PRO_G1_ISOLATED_LAB = $env:VERTEX_PRO_G1_ISOLATED_LAB
    VERTEX_PRO_G1_FAULT_MODE = $env:VERTEX_PRO_G1_FAULT_MODE
}

function Test-LoopbackPortAvailable {
    param([int]$Candidate)
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Candidate)
    try {
        $listener.Start()
        return $true
    }
    catch {
        return $false
    }
    finally {
        $listener.Stop()
    }
}

while (-not (Test-LoopbackPortAvailable -Candidate $Port)) {
    $Port++
    if ($Port -gt 65535) { throw 'no available loopback port for the isolated qui lab' }
}
$baseUrl = "http://127.0.0.1:$Port"

function Invoke-JsonRequest {
    param(
        [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
        [string]$Method,
        [string]$Uri,
        [object]$Body
    )
    $parameters = @{
        Method = $Method
        Uri = $Uri
        WebSession = $Session
        Headers = @{ Accept = 'application/json' }
        TimeoutSec = 30
    }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = ($Body | ConvertTo-Json -Compress -Depth 8)
    }
    Invoke-RestMethod @parameters
}

function Get-TemporaryPassword {
    param([string]$Service)
    $containerOutput = @(& docker compose -f $compose ps -q $Service)
    $container = ($containerOutput -join '').Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($container)) {
        $status = (& docker compose -f $compose ps $Service 2>&1 | Out-String).Trim()
        throw "Could not resolve a running container for $Service. Compose status: $status"
    }
    $logs = (& docker logs $container 2>&1 | Out-String)
    $match = [regex]::Match($logs, 'temporary password is provided for this session:\s+(\S+)', [System.Text.RegularExpressions.RegexOptions]::IgnoreCase)
    if (-not $match.Success) {
        throw "Temporary password was not found for $Service"
    }
    $match.Groups[1].Value
}

function Wait-HealthyQui {
    for ($attempt = 0; $attempt -lt 90; $attempt++) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/health" -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return }
        }
        catch { }
        Start-Sleep -Seconds 1
    }
    throw 'qui did not become healthy before the deadline'
}

function Assert-LabTasksEmpty {
    param([hashtable]$Profiles)
    foreach ($profile in $Profiles.Keys) {
        $key = $Profiles[$profile].proxy_key
        $proxyBase = "$baseUrl/proxy/$key/api/v2"
        try {
            $snapshot = Invoke-RestMethod -Method Get -Uri "$proxyBase/sync/maindata?rid=0" -TimeoutSec 30
            $torrentProperty = $null
            if ($null -ne $snapshot) {
                $torrentProperty = $snapshot.PSObject.Properties['torrents']
            }
            $hashes = @()
            if ($null -ne $torrentProperty -and $null -ne $torrentProperty.Value) {
                $hashes = @($torrentProperty.Value.PSObject.Properties | ForEach-Object { $_.Name })
            }
            if ($hashes.Count -ne 0) {
                throw "profile has $($hashes.Count) remaining task(s)"
            }
        }
        catch {
            throw "could not verify empty isolated qB task set for $profile"
        }
    }
}

Push-Location $root
$profiles = @{}
try {
    $env:VERTEX_G1_QUI_PORT = [string]$Port
    $upArguments = @('compose', '-f', $compose, 'up', '-d')
    if (-not $SkipBuild) { $upArguments += '--build' }
    & docker @upArguments
    if ($LASTEXITCODE -ne 0) { throw 'docker compose up failed' }
    Wait-HealthyQui

    $qb504Password = Get-TemporaryPassword -Service 'qb504'
    $qb522Password = Get-TemporaryPassword -Service 'qb522'
    $quiPassword = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(24)).TrimEnd('=')
    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    Invoke-JsonRequest -Session $session -Method Post -Uri "$baseUrl/api/auth/setup" -Body @{ username = 'admin'; password = $quiPassword } | Out-Null

    $definitions = @(
        @{ id = 'QB_438'; host = 'http://qb438:8080'; password = 'adminadmin' },
        @{ id = 'QB_504'; host = 'http://qb504:8080'; password = $qb504Password },
        @{ id = 'QB_522'; host = 'http://qb522:8080'; password = $qb522Password }
    )
    foreach ($definition in $definitions) {
        $instance = Invoke-JsonRequest -Session $session -Method Post -Uri "$baseUrl/api/instances/" -Body @{ name = $definition.id; host = $definition.host; username = 'admin'; password = $definition.password }
        $connected = $false
        for ($attempt = 0; $attempt -lt 45; $attempt++) {
            Start-Sleep -Seconds 1
            $instances = @(Invoke-JsonRequest -Session $session -Method Get -Uri "$baseUrl/api/instances/" -Body $null)
            $current = $instances | Where-Object { $_.id -eq $instance.id } | Select-Object -First 1
            if ($null -ne $current -and $current.connected) { $connected = $true; break }
        }
        if (-not $connected) { throw "$($definition.id) did not connect through qui" }
        $clientKey = Invoke-JsonRequest -Session $session -Method Post -Uri "$baseUrl/api/client-api-keys/" -Body @{ clientName = "vertex-g1-$($definition.id.ToLowerInvariant())"; instanceId = $instance.id }
        $profiles[$definition.id] = @{ proxy_key = $clientKey.key }
    }

    $env:VERTEX_PRO_QUI_URL = $baseUrl
    $env:VERTEX_PRO_G1_ISOLATED_LAB = $Confirmation
    $modes = @('none', 'response_loss', 'timeout', 'transport_202', 'transport_204', 'transport_409')
    foreach ($profile in @('QB_438', 'QB_504', 'QB_522')) {
        $env:VERTEX_PRO_QUI_PROXY_KEY = $profiles[$profile].proxy_key
        foreach ($mode in $modes) {
            $env:VERTEX_PRO_G1_FAULT_MODE = $mode
            $output = (& go run ./tools/qutransactionprobe 2>&1 | Out-String).Trim()
            if ($LASTEXITCODE -ne 0) { throw "transaction probe failed for ${profile}/${mode}: $output" }
            $result = $output | ConvertFrom-Json
            if ($result.status -ne 'PASS' -or $result.profile -ne $profile) { throw "unexpected probe result for $profile/$mode" }
            if (-not $result.transaction.durable_marker_before_send -or -not $result.transaction.response_fact_recorded) {
                throw "dispatch boundary evidence missing for $profile/$mode"
            }
            $suffix = switch ($mode) {
                'none' { 'transaction-flow' }
                'response_loss' { 'response-loss-reconcile' }
                'timeout' { 'timeout-no-effect' }
                'transport_202' { 'transport-202' }
                'transport_204' { 'transport-204' }
                'transport_409' { 'transport-409' }
            }
            $profileName = $profile.ToLowerInvariant().Replace('_', '')
            Set-Content -LiteralPath (Join-Path $evidenceRoot "lab-$profileName-$suffix.json") -Value $output -Encoding utf8
        }

        $env:VERTEX_PRO_G1_FAULT_MODE = 'external_kill'
        $killOutput = (& go run ./tools/qutransactionprobe 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw "external kill probe failed for ${profile}: $killOutput" }
        $killResult = $killOutput | ConvertFrom-Json
        if ($killResult.status -ne 'PASS' -or $killResult.profile -ne $profile) { throw "unexpected external kill result for $profile" }
        if (-not $killResult.fault.child_terminated -or -not $killResult.fault.child_exited_abruptly -or -not $killResult.fault.durable_marker_before_send) {
            throw "external kill boundary evidence missing for $profile"
        }
        if (-not $killResult.fault.startup_recovery_promoted_unknown -or -not $killResult.fault.replay_blocked) {
            throw "external kill recovery evidence missing for $profile"
        }
        if ([int]$killResult.fault.sender_calls -ne 1 -or [int]$killResult.fault.automatic_redispatches -ne 0) {
            throw "external kill replayed the Add for $profile"
        }
        $killProfileName = $profile.ToLowerInvariant().Replace('_', '')
        Set-Content -LiteralPath (Join-Path $evidenceRoot "lab-$killProfileName-external-kill.json") -Value $killOutput -Encoding utf8

        $env:VERTEX_PRO_G1_FAULT_MODE = 'fixture_capture'
        $env:VERTEX_PRO_FIXTURE_DIR = Join-Path $evidenceRoot 'fixtures'
        $fixtureOutput = (& go run ./tools/qutransactionprobe 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) { throw "fixture capture failed for ${profile}: $fixtureOutput" }
        $fixtureResult = $fixtureOutput | ConvertFrom-Json
        if ($fixtureResult.status -ne 'PASS' -or $fixtureResult.profile -ne $profile) { throw "unexpected fixture capture result for $profile" }
        foreach ($fixtureKind in @('add', 'sync', 'tracker', 'transfer_info')) {
            $digestProperty = $fixtureResult.fixtures.PSObject.Properties[$fixtureKind]
            if ($null -eq $digestProperty -or [string]::IsNullOrWhiteSpace($digestProperty.Value)) {
                throw "fixture digest missing for $profile/$fixtureKind"
            }
        }
        Set-Content -LiteralPath (Join-Path $evidenceRoot "lab-$killProfileName-fixture-capture.json") -Value $fixtureOutput -Encoding utf8
        Remove-Item Env:VERTEX_PRO_G1_FAULT_MODE -ErrorAction SilentlyContinue
        Remove-Item Env:VERTEX_PRO_FIXTURE_DIR -ErrorAction SilentlyContinue
    }

    & go test ./internal/qbresponse ./internal/addfacts ./internal/dispatchflow -run 'TestAdaptLegacyAndJSONResponsesNeverConfirmsHashSeen|TestRunnerCommitsSyntheticResponseFacts' -count=1
    if ($LASTEXITCODE -ne 0) { throw 'synthetic response fact suite failed' }

    Assert-LabTasksEmpty -Profiles $profiles
    & (Join-Path $PSScriptRoot 'summarize-three-profile.ps1') -EvidenceRoot $evidenceRoot | Out-Null
    Write-Output '{"status":"PASS","profiles":["QB_438","QB_504","QB_522"],"modes":["none","response_loss","timeout","transport_202","transport_204","transport_409"],"durable_marker_before_send":true,"secrets_persisted":false}'
}
finally {
    Remove-Item Env:VERTEX_G1_QUI_PORT -ErrorAction SilentlyContinue
    Remove-Item Env:VERTEX_PRO_QUI_URL -ErrorAction SilentlyContinue
    Remove-Item Env:VERTEX_PRO_QUI_PROXY_KEY -ErrorAction SilentlyContinue
    Remove-Item Env:VERTEX_PRO_G1_ISOLATED_LAB -ErrorAction SilentlyContinue
    Remove-Item Env:VERTEX_PRO_G1_FAULT_MODE -ErrorAction SilentlyContinue
    foreach ($entry in $previousEnvironment.GetEnumerator()) {
        if ($null -eq $entry.Value) {
            Remove-Item -Path "Env:$($entry.Key)" -ErrorAction SilentlyContinue
        }
        else {
            Set-Item -Path "Env:$($entry.Key)" -Value $entry.Value
        }
    }
    if (-not $KeepLab) {
        & docker compose -f $compose down --volumes --remove-orphans | Out-Null
    }
    Pop-Location
}
