#requires -Version 5.1
<#
.SYNOPSIS
Build the Windows desktop executable without launching it.
.PARAMETER SkipFrontend
Use an existing frontend/dist/index.html instead of running npm ci and npm run build.
#>
[CmdletBinding()]
param([switch]$SkipFrontend)

. (Join-Path $PSScriptRoot 'windows-common.ps1')
if ($env:OS -ne 'Windows_NT') {
    throw 'Run this script on Windows.'
}
$projectRoot = Split-Path -Parent $PSScriptRoot
$go = Get-RequiredApplication -Names @('go.exe', 'go')

Push-Location -LiteralPath $projectRoot
try {
    Invoke-Checked -Executable $go -Arguments @('version')
    $target = (Invoke-Checked -Executable $go -Arguments @('env', 'GOOS') | Out-String).Trim()
    if ($target -ne 'windows') {
        throw "GOOS must be windows for this build (current: $target)."
    }
    if ($SkipFrontend) {
        if (-not (Test-Path -LiteralPath (Join-Path $projectRoot 'frontend/dist/index.html') -PathType Leaf)) {
            throw '-SkipFrontend requires an existing frontend/dist/index.html. Run the script without -SkipFrontend first.'
        }
        Write-Host 'Using existing frontend/dist assets (-SkipFrontend); frontend checks were not run.'
    }
    else {
        Build-Frontend -ProjectRoot $projectRoot
    }
    $binPath = Join-Path $projectRoot 'bin'
    New-Item -ItemType Directory -Path $binPath -Force | Out-Null
    $outputPath = Join-Path $binPath 'surplustoken-connect.exe'
    Invoke-Checked -Executable $go -Arguments @('build', '-tags', 'production', '-trimpath', '-ldflags', '-H=windowsgui', '-o', $outputPath, '.')
    Write-Host "Built $outputPath (unsigned executable; not an installer)."
}
finally {
    Pop-Location
}
