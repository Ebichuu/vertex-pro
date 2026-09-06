[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $root
try {
    $go = Get-Command go -ErrorAction SilentlyContinue
    $node = Get-Command node -ErrorAction SilentlyContinue
    $pnpm = Get-Command pnpm -ErrorAction SilentlyContinue
    $checks = @(
        [ordered]@{ name = 'go'; expected = '1.26.6'; actual = if ($go) { (& $go.Source version) } else { 'NOT_FOUND' } },
        [ordered]@{ name = 'node'; expected = '24.14.0'; actual = if ($node) { (& $node.Source --version) } else { 'NOT_FOUND' } },
        [ordered]@{ name = 'pnpm'; expected = '10.33.0'; actual = if ($pnpm) { (& $pnpm.Source --version) } else { 'NOT_FOUND' } }
    )
    $failedToolchain = @($checks | Where-Object {
        ($_.name -eq 'go' -and $_.actual -notmatch 'go1\.26\.6') -or
        ($_.name -eq 'node' -and $_.actual -ne 'v24.14.0') -or
        ($_.name -eq 'pnpm' -and $_.actual -ne '10.33.0')
    })

    $layoutScript = Join-Path $root 'tools/contractcheck/check-repository-layout.ps1'
    $licenseScript = Join-Path $root 'tools/licensecheck/verify-license.ps1'
    & powershell -NoProfile -ExecutionPolicy Bypass -File $layoutScript | Out-Null
    $layoutExit = $LASTEXITCODE

    $targetScripts = [ordered]@{
        'generate-check' = { & go run ./tools/codegen -check }
        'fmt-check' = { & powershell -NoProfile -ExecutionPolicy Bypass -File tools/dev/format.ps1 -Check }
        'lint' = { & go vet ./... }
        'test-unit' = {
            & go test ./... -count=1
            # exit would terminate the whole preflight before evidence is written
            if ($LASTEXITCODE -ne 0) { return }
            & pnpm --dir web test
        }
        'test-contracts' = { & go run ./tools/contractcheck }
        'test-integration' = { & go test ./internal/app ./internal/dispatchflow ./internal/resourcesapi ./internal/rssservice ./internal/syncwatcher -count=1 }
        'test-e2e' = { & go test ./cmd/vertex-pro ./internal/httpapi ./internal/resourcesapi ./internal/sseserver -count=1 }
        'test-protocol' = { & go test ./internal/qui/... ./internal/qbadd ./internal/qbprofile ./internal/qbresponse ./internal/syncmerge -count=1 }
        'test-fault' = { & go test ./internal/addfaultmatrix ./internal/dispatchcommit ./internal/qbexecutor ./internal/saferetry ./internal/startuprecover ./internal/unknownreconciler -count=1 }
        'verify-license' = { & powershell -NoProfile -ExecutionPolicy Bypass -File tools/licensecheck/verify-license.ps1 }
        'verify-implementation-plan' = { & powershell -NoProfile -ExecutionPolicy Bypass -File tools/dev/verify-implementation-plan.ps1 }
    }
    $targetResults = @()
    foreach ($target in $targetScripts.Keys) {
        $output = ''
        $exitCode = 0
        try {
            $output = (& $targetScripts[$target] 2>&1 | Out-String).Trim()
            $exitCode = $LASTEXITCODE
        }
        catch {
            $output = $_.Exception.Message
            $exitCode = 1
        }
        $targetResults += [ordered]@{
            name = $target
            status = if ($exitCode -eq 0) { 'PASS' } else { 'FAIL' }
            exit_code = $exitCode
            output_tail = if ($output.Length -gt 1000) { $output.Substring($output.Length - 1000) } else { $output }
        }
    }
    $blockedTargets = @()
    $failedTargets = @($targetResults | Where-Object { $_.status -ne 'PASS' })
    $status = if ($failedToolchain.Count -eq 0 -and $layoutExit -eq 0 -and $failedTargets.Count -eq 0 -and $blockedTargets.Count -eq 0) { 'PASS' } else { 'BLOCKED' }
    $result = [ordered]@{
        check = 'BOOT-05 preflight shell'
        status = $status
        toolchain = $checks
        layout_exit = $layoutExit
        targets = $targetResults
        blocked_targets = $blockedTargets
    }
    $json = $result | ConvertTo-Json -Depth 8
    $json
    Set-Content -LiteralPath (Join-Path $root 'docs/evidence/boot-05/preflight-check.json') -Value $json -Encoding utf8
    if ($status -ne 'PASS') { exit 2 }
}
finally {
    Pop-Location
}
