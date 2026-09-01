[CmdletBinding()]
param()
Set-StrictMode -Version Latest
$ErrorActionPreference='Stop'
$required=@('Makefile','tools/dev/bootstrap.ps1','tools/dev/preflight.ps1','tools/dev/not-implemented.ps1')
$missing=@($required|Where-Object{-not(Test-Path -LiteralPath $_)})
$result=[ordered]@{check='BOOT-05 command shell';status=if($missing.Count -eq 0){'PASS'}else{'FAIL'};missing=$missing}
$result|ConvertTo-Json -Depth 4
if($missing.Count){exit 1}
