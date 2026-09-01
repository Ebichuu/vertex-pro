[CmdletBinding()]
param([Parameter(Mandatory=$true)][string]$Name)
Set-StrictMode -Version Latest
Write-Error "目标 '$Name' 尚未实现；为避免伪造通过结果，本命令明确失败。"
exit 2
