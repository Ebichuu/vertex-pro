[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$statusPath = Join-Path $root 'docs/实施流程/IMPLEMENTATION_STATUS_V1.md'
$lines = @(Get-Content -LiteralPath $statusPath -Encoding UTF8)
$rows = @()
$errors = @()
foreach ($line in $lines) {
    if (-not $line.StartsWith('|')) { continue }
    $parts = $line.Split('|')
    if ($parts.Count -lt 9) { continue }
    $idCell = $parts[1].Trim()
    if ($idCell -notmatch '^`(?<id>[A-Z][A-Z0-9]+-\d{2})`$') { continue }
    $id = $Matches.id
    $status = $parts[4].Trim().Trim('`')
    $depends = $parts[3].Trim()
    $evidence = $parts[7].Trim()
    $rows += [pscustomobject]@{ Id = $id; Status = $status; Depends = $depends; Evidence = $evidence }
}
if ($rows.Count -ne 149) {
    $errors += "implementation item count is $($rows.Count), want 149"
}
$ids = @{}
foreach ($row in $rows) {
    if ($ids.ContainsKey($row.Id)) {
        $errors += "duplicate implementation id: $($row.Id)"
    }
    $ids[$row.Id] = $true
    if ($row.Status -notin @('PENDING', 'IN_PROGRESS', 'IN_REVIEW', 'BLOCKED', 'DONE')) {
        $errors += "$($row.Id) has invalid status $($row.Status)"
    }
    if ($row.Status -eq 'DONE' -and ($row.Evidence -eq '' -or $row.Evidence -eq '—' -or $row.Evidence -eq '-')) {
        $errors += "$($row.Id) is DONE without evidence"
    }
}
$dependencies = @{}
foreach ($row in $rows) {
    $dependencies[$row.Id] = @()
    $raw = $row.Depends.Replace('`', '').Trim()
    if ($raw -eq '' -or $raw -eq '—' -or $raw -eq '-') { continue }
    $inheritedPrefix = ''
    foreach ($tokenValue in $raw.Split(',')) {
        $token = $tokenValue.Trim()
        if ($token -eq '') { continue }
        if ($token -match '^(?<prefix>[A-Z][A-Z0-9]+)-(?<number>\d{2})$') {
            $inheritedPrefix = $Matches.prefix
            $dependencies[$row.Id] += "$($Matches.prefix)-$($Matches.number)"
            continue
        }
        if ($token -match '^(?<number>\d{2})$' -and $inheritedPrefix -ne '') {
            $dependencies[$row.Id] += "$inheritedPrefix-$($Matches.number)"
            continue
        }
        if ($token -match '^(?<prefix>[A-Z][A-Z0-9]+)-(?<start>\d{2})\.\.(?:(?<endPrefix>[A-Z][A-Z0-9]+)-)?(?<end>\d{2})$') {
            $prefix = $Matches.prefix
            $endPrefix = if ($Matches.ContainsKey('endPrefix') -and $Matches.endPrefix) { $Matches.endPrefix } else { $prefix }
            if ($endPrefix -ne $prefix) {
                $errors += "$($row.Id) has a cross-prefix dependency range: $token"
                continue
            }
            $start = [int]$Matches.start
            $end = [int]$Matches.end
            if ($end -lt $start) {
                $errors += "$($row.Id) has a reversed dependency range: $token"
                continue
            }
            for ($number = $start; $number -le $end; $number++) {
                $dependencies[$row.Id] += ('{0}-{1:D2}' -f $prefix, $number)
            }
            continue
        }
        $errors += "$($row.Id) has invalid dependency expression: $token"
    }
    foreach ($dependency in $dependencies[$row.Id]) {
        if (-not $ids.ContainsKey($dependency)) {
            $errors += "$($row.Id) depends on unknown item: $dependency"
        }
    }
}
$visiting = @{}
$visited = @{}
function Visit-ImplementationItem([string]$id) {
    if ($visiting.ContainsKey($id)) {
        throw "dependency cycle detected at $id"
    }
    if ($visited.ContainsKey($id)) { return }
    $visiting[$id] = $true
    foreach ($dependency in $dependencies[$id]) {
        Visit-ImplementationItem $dependency
    }
    $visiting.Remove($id)
    $visited[$id] = $true
}
foreach ($row in $rows) {
    try {
        Visit-ImplementationItem $row.Id
    }
    catch {
        $errors += $_.Exception.Message
        break
    }
}
$result = [ordered]@{
    check = 'implementation-plan'
    status = if ($errors.Count -eq 0) { 'PASS' } else { 'ERROR' }
    item_count = $rows.Count
    expected_item_count = 149
    error_count = $errors.Count
    errors = $errors
}
$result | ConvertTo-Json -Depth 5
if ($errors.Count -gt 0) { exit 1 }
