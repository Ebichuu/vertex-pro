[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$root=(Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$goVersion = (Get-Command go -ErrorAction SilentlyContinue)
$nodeVersion = (Get-Command node -ErrorAction SilentlyContinue)
$pnpmVersion = (Get-Command pnpm -ErrorAction SilentlyContinue)
if($null -eq $goVersion -or $null -eq $nodeVersion -or $null -eq $pnpmVersion){throw 'Go、Node 或 pnpm 未安装'}
pnpm install --frozen-lockfile --ignore-scripts
Write-Output 'bootstrap=PASS'
