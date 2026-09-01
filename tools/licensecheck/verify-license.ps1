[CmdletBinding()]
param(
    [string]$RepositoryRoot
)

if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
}

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$licensePath = Join-Path $RepositoryRoot 'LICENSE'
$noticesPath = Join-Path $RepositoryRoot 'THIRD_PARTY_NOTICES.md'
$contributingPath = Join-Path $RepositoryRoot 'CONTRIBUTING.md'
$requiredMarkers = @(
    'GNU GENERAL PUBLIC LICENSE',
    'Version 2, June 1991',
    'END OF TERMS AND CONDITIONS'
)
$licenseText = Get-Content -LiteralPath $licensePath -Raw -Encoding utf8
$missingMarkers = @($requiredMarkers | Where-Object { $licenseText -notlike "*$($_)*" })
$missingFiles = @(@($licensePath, $noticesPath, $contributingPath) | Where-Object { -not (Test-Path -LiteralPath $_) })
$result = [ordered]@{
    check = 'BOOT-03 license files'
    status = if ($missingMarkers.Count -eq 0 -and $missingFiles.Count -eq 0) { 'PASS' } else { 'FAIL' }
    license = if ($missingMarkers.Count -eq 0) { 'GPL-2.0 text markers present' } else { 'INVALID' }
    missing_files = @($missingFiles | ForEach-Object { $_.Substring($RepositoryRoot.Length + 1) })
    missing_markers = $missingMarkers
}
$result | ConvertTo-Json -Depth 4
if ($result.status -ne 'PASS') { exit 1 }

