[CmdletBinding()]
param(
    [string]$EvidenceRoot,
    [string]$OutputPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
if ([string]::IsNullOrWhiteSpace($EvidenceRoot)) {
    $EvidenceRoot = Join-Path $root 'docs/evidence/qui-12'
}
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $EvidenceRoot 'g1-three-profile-matrix-summary.json'
}

$profiles = @('QB_438', 'QB_504', 'QB_522')
$modes = [ordered]@{
    none = 'transaction-flow'
    response_loss = 'response-loss-reconcile'
    timeout = 'timeout-no-effect'
    transport_202 = 'transport-202'
    transport_204 = 'transport-204'
    transport_409 = 'transport-409'
}
$expected = @{
    none = @{ commit = 'AWAITING_CONFIRMATION'; final = 'HASH_SEEN'; reservation = 'REFLECTING'; binding = $true }
    response_loss = @{ commit = 'UNKNOWN'; final = 'RECONCILED_CONFIRMED'; reservation = 'REFLECTING'; binding = $true }
    timeout = @{ commit = 'UNKNOWN'; final = 'RECONCILED_NO_EFFECT'; reservation = 'RELEASED'; binding = $false }
    transport_202 = @{ commit = 'AWAITING_CONFIRMATION'; final = 'HASH_SEEN'; reservation = 'REFLECTING'; binding = $true }
    transport_204 = @{ commit = 'UNKNOWN'; final = 'RECONCILED_NO_EFFECT'; reservation = 'RELEASED'; binding = $false }
    transport_409 = @{ commit = 'UNKNOWN'; final = 'RECONCILED_NO_EFFECT'; reservation = 'RELEASED'; binding = $false }
}

function Assert-Equal {
    param([object]$Actual, [object]$Expected, [string]$Fact)
    if ($Actual -ne $Expected) {
        throw "$Fact was '$Actual', expected '$Expected'"
    }
}

$reports = @()
$allHaveDurableMarkerEvidence = $true
foreach ($profile in $profiles) {
    $profileName = $profile.ToLowerInvariant().Replace('_', '')
    foreach ($mode in $modes.Keys) {        $fileName = "lab-$profileName-$($modes[$mode]).json"
        $path = Join-Path $EvidenceRoot $fileName
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "missing G1 matrix evidence: $fileName"
        }
        $record = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
        Assert-Equal $record.status 'PASS' "$fileName status"
        Assert-Equal $record.profile $profile "$fileName profile"
        Assert-Equal $record.fault.mode $mode "$fileName mode"
        Assert-Equal ([int]$record.fault.sender_calls) 1 "$fileName sender_calls"
        Assert-Equal ([int]$record.fault.automatic_redispatches) 0 "$fileName automatic_redispatches"
        Assert-Equal ([bool]$record.safety.production_services_called) $false "$fileName production_services_called"
        Assert-Equal ([bool]$record.safety.lab_returned_empty) $true "$fileName lab_returned_empty"
        Assert-Equal ([bool]$record.safety.temporary_database_removed) $true "$fileName temporary_database_removed"
        Assert-Equal ([bool]$record.safety.secrets_redacted) $true "$fileName secrets_redacted"
        Assert-Equal ([bool]$record.placement_gate.selected) $false "$fileName placement selected"
        Assert-Equal $record.placement_gate.profile_state 'EXPECTED' "$fileName profile state"
        Assert-Equal $record.placement_gate.rejection_code 'PROFILE_NOT_VERIFIED' "$fileName placement rejection"
        Assert-Equal $record.transaction.dispatch_commit_state $expected[$mode].commit "$fileName dispatch commit"
        Assert-Equal $record.transaction.attempt_final $expected[$mode].final "$fileName attempt final"
        Assert-Equal $record.transaction.job_final $expected[$mode].final "$fileName job final"
        Assert-Equal $record.transaction.reservation_final $expected[$mode].reservation "$fileName reservation final"
        Assert-Equal ([bool]$record.transaction.binding_created) $expected[$mode].binding "$fileName binding"
        Assert-Equal $record.transaction.intent_dispatch 'STARTED' "$fileName intent dispatch"
        Assert-Equal ([bool]$record.transaction.waiter_closed) $true "$fileName waiter closed"

        $markerProperty = $record.transaction.PSObject.Properties['durable_marker_before_send']
        $responseProperty = $record.transaction.PSObject.Properties['response_fact_recorded']
        $hasCurrentBoundaryEvidence = $null -ne $markerProperty -and $null -ne $responseProperty -and [bool]$markerProperty.Value -and [bool]$responseProperty.Value
        if (-not $hasCurrentBoundaryEvidence) {
            $allHaveDurableMarkerEvidence = $false
        }
        $reports += [pscustomobject][ordered]@{
            profile = $profile
            mode = $mode
            status = 'PASS'
            sender_calls = [int]$record.fault.sender_calls
            automatic_redispatches = [int]$record.fault.automatic_redispatches
            dispatch_commit_state = $record.transaction.dispatch_commit_state
            attempt_final = $record.transaction.attempt_final
            lab_returned_empty = [bool]$record.safety.lab_returned_empty
            durable_marker_before_send = if ($null -ne $markerProperty) { [bool]$markerProperty.Value } else { $null }
            response_fact_recorded = if ($null -ne $responseProperty) { [bool]$responseProperty.Value } else { $null }
            path = "docs/evidence/qui-12/$fileName"
            sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
}

$killReports = @()
foreach ($profile in $profiles) {
    $profileName = $profile.ToLowerInvariant().Replace('_', '')
    $fileName = "lab-$profileName-external-kill.json"
    $path = Join-Path $EvidenceRoot $fileName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "missing external kill -9 evidence: $fileName"
    }
    $record = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
    Assert-Equal $record.status 'PASS' "$fileName status"
    Assert-Equal $record.profile $profile "$fileName profile"
    Assert-Equal $record.fault.mode 'external_kill' "$fileName mode"
    Assert-Equal ([int]$record.fault.sender_calls) 1 "$fileName sender_calls"
    Assert-Equal ([int]$record.fault.automatic_redispatches) 0 "$fileName automatic_redispatches"
    Assert-Equal ([bool]$record.fault.child_terminated) $true "$fileName child_terminated"
    Assert-Equal ([bool]$record.fault.child_exited_abruptly) $true "$fileName child_exited_abruptly"
    Assert-Equal ([bool]$record.fault.durable_marker_before_send) $true "$fileName durable_marker_before_send"
    Assert-Equal ([bool]$record.fault.startup_recovery_promoted_unknown) $true "$fileName startup_recovery_promoted_unknown"
    Assert-Equal ([bool]$record.fault.replay_blocked) $true "$fileName replay_blocked"
    Assert-Equal $record.fault.reconciler_outcome 'RECONCILED_CONFIRMED' "$fileName reconciler_outcome"
    Assert-Equal ([bool]$record.safety.production_services_called) $false "$fileName production_services_called"
    Assert-Equal ([bool]$record.safety.lab_returned_empty) $true "$fileName lab_returned_empty"
    Assert-Equal ([bool]$record.safety.temporary_database_removed) $true "$fileName temporary_database_removed"
    Assert-Equal ([bool]$record.safety.secrets_redacted) $true "$fileName secrets_redacted"
    Assert-Equal $record.placement_gate.profile_state 'EXPECTED' "$fileName profile state"
    Assert-Equal $record.transaction.attempt_final 'RECONCILED_CONFIRMED' "$fileName attempt final"
    Assert-Equal $record.transaction.intent_dispatch 'STARTED' "$fileName intent dispatch"
    Assert-Equal ([bool]$record.transaction.response_fact_recorded) $false "$fileName response fact must stay NULL"
    Assert-Equal ([bool]$record.transaction.waiter_closed) $true "$fileName waiter closed"
    $killReports += [pscustomobject][ordered]@{
        profile = $profile
        status = 'PASS'
        path = "docs/evidence/qui-12/$fileName"
        sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}

$fixtureCaptures = @()
foreach ($profile in $profiles) {
    $profileName = $profile.ToLowerInvariant().Replace('_', '')
    $fileName = "lab-$profileName-fixture-capture.json"
    $path = Join-Path $EvidenceRoot $fileName
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "missing fixture capture evidence: $fileName"
    }
    $record = Get-Content -LiteralPath $path -Raw -Encoding utf8 | ConvertFrom-Json
    Assert-Equal $record.status 'PASS' "$fileName status"
    Assert-Equal $record.profile $profile "$fileName profile"
    Assert-Equal ([bool]$record.safety.secrets_redacted) $true "$fileName secrets_redacted"
    Assert-Equal ([bool]$record.safety.lab_returned_empty) $true "$fileName lab_returned_empty"
    $fixtureDigests = [ordered]@{}
    foreach ($fixtureKind in @('add', 'sync', 'tracker', 'transfer_info')) {
        $digest = $record.fixtures.PSObject.Properties[$fixtureKind].Value
        if ([string]::IsNullOrWhiteSpace($digest)) {
            throw "fixture digest missing in $fileName for $fixtureKind"
        }
        $fixturePath = Join-Path $EvidenceRoot "fixtures/fixture-$profileName-$fixtureKind.json"
        if (-not (Test-Path -LiteralPath $fixturePath -PathType Leaf)) {
            throw "missing fixture file: fixture-$profileName-$fixtureKind.json"
        }
        $actual = (Get-FileHash -LiteralPath $fixturePath -Algorithm SHA256).Hash.ToLowerInvariant()
        Assert-Equal $actual $digest "fixture-$profileName-$fixtureKind digest"
        $fixtureDigests[$fixtureKind] = $actual
    }
    $fixtureCaptures += [pscustomobject][ordered]@{
        profile = $profile
        status = 'PASS'
        digests = $fixtureDigests
        capture_report = "docs/evidence/qui-12/$fileName"
        capture_report_sha256 = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
    }
}

$gitCommit = (& git -C $root rev-parse HEAD 2>$null | Out-String).Trim()
$gitStatus = (& git -C $root status --porcelain 2>$null | Out-String)
$summary = [ordered]@{
    item = 'QUI-12 / ADD-05..14'
    status = 'IN_PROGRESS'
    generated_at = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
    source = [ordered]@{
        git_commit = $gitCommit
        git_clean = [string]::IsNullOrWhiteSpace($gitStatus)
        report_count = $reports.Count
    }
    archived_matrix = [ordered]@{
        status = 'PASS'
        profiles = $profiles
        modes = @($modes.Keys)
        expected_reports = 18
        passed_reports = @($reports | Where-Object { $_.status -eq 'PASS' }).Count
        total_sender_calls = [int](($reports | Measure-Object -Property sender_calls -Sum).Sum)
        automatic_redispatches = [int](($reports | Measure-Object -Property automatic_redispatches -Sum).Sum)
        all_labs_returned_empty = (@($reports | Where-Object { -not $_.lab_returned_empty }).Count -eq 0)
        reports = $reports
    }
    current_dispatch_boundary = [ordered]@{
        durable_marker_before_send_evidenced_by_all_reports = $allHaveDurableMarkerEvidence
        current_code_revalidated_in_three_profile_lab = $allHaveDurableMarkerEvidence
        status = if ($allHaveDurableMarkerEvidence) { 'PASS' } else { 'PENDING_REVALIDATION' }
        note = if ($allHaveDurableMarkerEvidence) {
            'All reports were produced after the pre-send durable marker was added.'
        }
        else {
            'The archived 18-case matrix predates the pre-send durable marker evidence fields and must be rerun against the current code.'
        }
    }
    external_kill_matrix = [ordered]@{
        status = if (@($killReports).Count -eq 3) { 'PASS' } else { 'FAIL' }
        expected_reports = 3
        passed_reports = @($killReports).Count
        automatic_redispatches = 0
        reports = $killReports
    }
    golden_fixtures = [ordered]@{
        status = if (@($fixtureCaptures).Count -eq 3) { 'PASS' } else { 'FAIL' }
        expected_profiles = 3
        captured_profiles = @($fixtureCaptures).Count
        captures = $fixtureCaptures
    }
    verification_decision = [ordered]@{
        profile_state = 'EXPECTED'
        may_promote_to_verified = $false
        g1_gate = 'PENDING'
    }
    remaining_before_profile_verified = @(
        'Pin the transaction harness image and emit a complete immutable image/source digest manifest.',
        'Run the P0 matrix (including external kill -9) three consecutive times from a clean commit and record profile_verification_runs for all repetitions with the archived fixture digests.'
    )
}

$json = $summary | ConvertTo-Json -Depth 12
Set-Content -LiteralPath $OutputPath -Value $json -Encoding utf8
Write-Output $OutputPath
