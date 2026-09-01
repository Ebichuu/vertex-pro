[CmdletBinding()]
param(
    [string]$GOPROXY = 'https://goproxy.cn,direct'
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$volume = "${root}:/src"
& docker run --rm --network host -e "GOPROXY=$GOPROXY" -v $volume -w /src golang:1.26.3-bookworm go test -race ./... -count=1
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
