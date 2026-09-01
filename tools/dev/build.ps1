[CmdletBinding()]
param(
    [string]$Output = 'dist/vertex-pro.exe'
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $root
try {
    & pnpm --dir web build
    $embedRoot = Join-Path $root 'internal/webembed/dist'
    if (Test-Path -LiteralPath $embedRoot) {
        Remove-Item -LiteralPath $embedRoot -Recurse -Force
    }
    New-Item -ItemType Directory -Path $embedRoot -Force | Out-Null
    Copy-Item -Path (Join-Path $root 'web/dist/*') -Destination $embedRoot -Recurse -Force
    $outputPath = Join-Path $root $Output
    New-Item -ItemType Directory -Path (Split-Path -Parent $outputPath) -Force | Out-Null
    & go build -trimpath -o $outputPath ./cmd/vertex-pro
    Write-Output "built $outputPath"
}
finally {
    Pop-Location
}
