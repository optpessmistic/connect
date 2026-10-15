#requires -Version 5.1
<#
.SYNOPSIS
Run Go package tests and the locked frontend build on Windows.
.PARAMETER SkipFrontend
Skip npm dependency installation, frontend tests, TypeScript checks, and frontend build.
.PARAMETER RequireRace
Fail if the race detector cannot run; otherwise report its unavailability and run ordinary Go tests.
#>
[CmdletBinding()]
param(
    [switch]$SkipFrontend,
    [switch]$RequireRace
)

. (Join-Path $PSScriptRoot 'windows-common.ps1')
if ($env:OS -ne 'Windows_NT') {
    throw 'Run this script on Windows.'
}
$projectRoot = Split-Path -Parent $PSScriptRoot
$go = Get-RequiredApplication -Names @('go.exe', 'go')

Push-Location -LiteralPath $projectRoot
try {
    Invoke-Checked -Executable $go -Arguments @('version')
    $raceReason = Get-RaceUnavailableReason -Go $go
    if ($raceReason) {
        if ($RequireRace) {
            throw "Race testing is required but unavailable: $raceReason."
        }
        Write-Warning "Race detector NOT run: $raceReason. Running ordinary Go tests only."
        Invoke-Checked -Executable $go -Arguments @('test', '-count=1', './internal/...', './cmd/...')
    }
    else {
        Write-Host 'Running Go tests with the race detector.'
        Invoke-Checked -Executable $go -Arguments @('test', '-race', '-count=1', './internal/...', './cmd/...')
    }
    Invoke-Checked -Executable $go -Arguments @('vet', './internal/...', './cmd/...')
    if ($SkipFrontend) {
        Write-Host 'Frontend checks were not run (-SkipFrontend).'
    }
    else {
        Build-Frontend -ProjectRoot $projectRoot -RunTests
    }
}
finally {
    Pop-Location
}
