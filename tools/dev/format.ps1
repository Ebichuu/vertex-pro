[CmdletBinding()]
param(
    [switch]$Check
)
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$root = (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$files = @(Get-ChildItem -LiteralPath $root -Recurse -File -Filter '*.go' | Where-Object {
    $_.FullName -notmatch '[\\/]vendor[\\/]' -and $_.FullName -notmatch '[\\/]node_modules[\\/]'
})
if ($Check) {
    $unformatted = @()
    foreach ($file in $files) {
        $output = (& gofmt -l $file.FullName)
        if ($output) { $unformatted += $output }
    }
    if ($unformatted.Count -gt 0) {
        $unformatted | ForEach-Object { Write-Error "unformatted Go file: $_" }
        exit 1
    }
    exit 0
}
foreach ($file in $files) {
    & gofmt -w $file.FullName
}
