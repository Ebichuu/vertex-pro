[CmdletBinding()]
param(
    [string]$RepositoryRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
}

$layoutScript = Join-Path $RepositoryRoot 'tools/contractcheck/check-repository-layout.ps1'
$layoutOutput = & powershell -NoProfile -ExecutionPolicy Bypass -File $layoutScript
$layoutExitCode = $LASTEXITCODE
$gitDirectory = Test-Path -LiteralPath (Join-Path $RepositoryRoot '.git')
$previousErrorActionPreference = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
$head = git -C $RepositoryRoot rev-parse --verify HEAD 2>$null
$headExitCode = $LASTEXITCODE
$ErrorActionPreference = $previousErrorActionPreference
$hasHead = $headExitCode -eq 0
$codeownersText = Get-Content -LiteralPath (Join-Path $RepositoryRoot 'CODEOWNERS') -Raw -Encoding utf8
$activeCodeownerLines = @(
    $codeownersText -split '\r?\n' |
    Where-Object { $_.Trim() -and -not $_.Trim().StartsWith('#') }
)

$blockingReasons = [System.Collections.Generic.List[string]]::new()
if ($layoutExitCode -ne 0) { $blockingReasons.Add('repository_layout_check_failed') }
if (-not $gitDirectory) { $blockingReasons.Add('git_directory_missing') }
if (-not $hasHead) { $blockingReasons.Add('baseline_commit_missing') }
if ($activeCodeownerLines.Count -eq 0) { $blockingReasons.Add('codeowners_owner_missing') }

$result = [ordered]@{
    check = 'BOOT-01 readiness'
    repository_root = (Resolve-Path -LiteralPath $RepositoryRoot).Path
    layout_check = if ($layoutExitCode -eq 0) { 'PASS' } else { 'FAIL' }
    git_directory = if ($gitDirectory) { 'PASS' } else { 'FAIL' }
    baseline_commit = if ($hasHead) { $head.Trim() } else { 'MISSING' }
    codeowners = if ($activeCodeownerLines.Count -gt 0) { 'PASS' } else { 'MISSING_OWNER' }
    status = if ($blockingReasons.Count -eq 0) { 'PASS' } else { 'BLOCKED' }
    blocking_reasons = @($blockingReasons)
}

$result | ConvertTo-Json -Depth 5

if ($blockingReasons.Count -gt 0) {
    exit 2
}

