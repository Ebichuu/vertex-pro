[CmdletBinding()]
param(
    [string]$RepositoryRoot
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) { $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path }
$lockPath = Join-Path $RepositoryRoot 'third_party/qui/UPSTREAM_LOCK.yaml'
$lockText = Get-Content -LiteralPath $lockPath -Raw -Encoding utf8
$lockLines = Get-Content -LiteralPath $lockPath -Encoding utf8
$required = @('repository: https://github.com/autobrr/qui','commit: ','license: GPL-2.0-or-later','candidate_files:','file_digests:')
$missing = @($required | Where-Object { $lockText -notlike "*$($_)*" })
$commitMatch = [regex]::Match($lockText, '(?m)^commit: ([0-9a-f]{40})$')
$candidatePaths = @($lockLines | Where-Object { $_ -match '^  - path: ' } | ForEach-Object { $_ -replace '^  - path: ', '' })
$digestLines = @($lockLines | Where-Object { $_ -match '^  third_party/qui/imported/.+: ' })
$digestEntries = @($digestLines | Where-Object { ($_ -split ': ')[-1] -match '^[0-9a-f]{64}$' })
$invalidDigestEntries = @($digestLines | Where-Object { ($_ -split ': ')[-1] -notmatch '^[0-9a-f]{64}$' })
$missingCandidates = @($candidatePaths | Where-Object { -not (Test-Path -LiteralPath (Join-Path $RepositoryRoot ('docs/evidence/boot-04/upstream-files/' + ($_ -replace '/','__')))) })
$result = [ordered]@{
    check = 'BOOT-04 qui upstream lock'
    status = if($missing.Count -eq 0 -and $commitMatch.Success -and $candidatePaths.Count -eq 5 -and $missingCandidates.Count -eq 0 -and $digestEntries.Count -eq 5 -and $invalidDigestEntries.Count -eq 0){'PASS'}else{'FAIL'}
    commit = if($commitMatch.Success){$commitMatch.Groups[1].Value}else{'INVALID'}
    candidate_count = $candidatePaths.Count
    digest_count = $digestEntries.Count
    missing_candidates = $missingCandidates
    invalid_digest_entries = $invalidDigestEntries
    missing_markers = $missing
}
$result | ConvertTo-Json -Depth 4
if($result.status -ne 'PASS'){exit 1}