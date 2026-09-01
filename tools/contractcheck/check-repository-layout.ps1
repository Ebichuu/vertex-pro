[CmdletBinding()]
param(
    [string]$RepositoryRoot
)

if ([string]::IsNullOrWhiteSpace($RepositoryRoot)) {
    $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
}

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$requiredPaths = @(
    'README.md',
    'CODEOWNERS',
    '.gitignore',
    'artifacts/.gitkeep',
    'third_party/qui/UPSTREAM_LOCK.yaml',
    'cmd/vertex-pro',
    'internal/app',
    'internal/config',
    'internal/secrets',
    'internal/domain',
    'internal/state',
    'internal/qui/control',
    'internal/qui/proxy',
    'internal/qbprofile',
    'internal/syncwatcher',
    'internal/snapshot',
    'internal/rss',
    'internal/rules',
    'internal/torrent',
    'internal/race',
    'internal/placement',
    'internal/capacity',
    'internal/submit',
    'internal/history',
    'internal/stats',
    'internal/events',
    'internal/commands',
    'internal/auth',
    'internal/api',
    'internal/store/sqlite',
    'migrations',
    'api',
    'contracts',
    'web/src',
    'web/tests',
    'tests/contract',
    'tests/integration',
    'tests/protocol',
    'tests/fault',
    'tests/performance',
    'tests/fixtures',
    'tools/contractcheck',
    'tools/protocolprobe',
    'tools/fixture-redact',
    'tools/licensecheck',
    'deploy/docker',
    'deploy/compose/lab',
    'docs/spec',
    'docs/adr',
    'docs/implementation',
    'docs/runbook',
    'docs/evidence'
)

$missing = @(
    foreach ($relativePath in $requiredPaths) {
        $absolutePath = Join-Path $RepositoryRoot $relativePath
        if (-not (Test-Path -LiteralPath $absolutePath)) {
            $relativePath
        }
    }
)

$result = [ordered]@{
    check = 'BOOT-01 repository layout'
    repository_root = (Resolve-Path -LiteralPath $RepositoryRoot).Path
    required_path_count = $requiredPaths.Count
    missing_paths = $missing
    status = if ($missing.Count -eq 0) { 'PASS' } else { 'FAIL' }
}

$result | ConvertTo-Json -Depth 4

if ($missing.Count -gt 0) {
    exit 1
}

