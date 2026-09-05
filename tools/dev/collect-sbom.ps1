[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
Push-Location $root
try {
    $env:CGO_ENABLED = '0'
    & go build -trimpath -ldflags '-s -w' -o artifacts/vertex-pro ./cmd/vertex-pro
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & go run ./tools/sbom -binary artifacts/vertex-pro -web-package-json web/package.json -base-image gcr.io/distroless/static-debian12:nonroot -out docs/evidence/boot-08/sbom.json
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    Write-Output 'sbom written to docs/evidence/boot-08/sbom.json'
}
finally {
    Pop-Location
}
