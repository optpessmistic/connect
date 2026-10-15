#requires -Version 5.1

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
# Check native exit codes explicitly, consistently on Windows PowerShell and pwsh.
$PSNativeCommandUseErrorActionPreference = $false

function Get-RequiredApplication {
    param([Parameter(Mandatory = $true)][string[]]$Names)

    foreach ($name in $Names) {
        $command = Get-Command -Name $name -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($null -ne $command) {
            return $command.Source
        }
    }
    throw "Required tool was not found on PATH: $($Names -join ', '). Install it separately; this script does not install global tools."
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory = $true)][string]$Executable,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    & $Executable @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code $($LASTEXITCODE): $Executable $($Arguments -join ' ')"
    }
}

function Build-Frontend {
    param(
        [Parameter(Mandatory = $true)][string]$ProjectRoot,
        [switch]$RunTests
    )

    $node = Get-RequiredApplication -Names @('node.exe', 'node')
    $npm = Get-RequiredApplication -Names @('npm.cmd', 'npm')
    Invoke-Checked -Executable $node -Arguments @('--version')
    $frontendPath = Join-Path $ProjectRoot 'frontend'
    Invoke-Checked -Executable $npm -Arguments @('--prefix', $frontendPath, 'ci', '--no-audit', '--no-fund')
    if ($RunTests) {
        Invoke-Checked -Executable $npm -Arguments @('--prefix', $frontendPath, 'test')
    }
    Invoke-Checked -Executable $npm -Arguments @('--prefix', $frontendPath, 'run', 'build')
}

function Get-RaceUnavailableReason {
    param([Parameter(Mandatory = $true)][string]$Go)

    $settings = (Invoke-Checked -Executable $Go -Arguments @('env', '-json', 'CGO_ENABLED', 'CC', 'GOOS', 'GOARCH') | Out-String) | ConvertFrom-Json
    if ($settings.GOOS -ne 'windows' -or $settings.GOARCH -ne 'amd64') {
        return "the Windows race detector requires GOOS=windows and GOARCH=amd64 (current: $($settings.GOOS)/$($settings.GOARCH))"
    }
    if ($settings.CGO_ENABLED -ne '1') {
        return 'CGO_ENABLED is not 1; race testing requires CGO and a compatible C compiler'
    }
    # CC may be a path containing spaces, a quoted path, or a compiler wrapper.
    $compiler = [string]$settings.CC
    if (Test-Path -LiteralPath $compiler -PathType Leaf) {
        return $null
    }
    if ($compiler -match '^"([^"]+)"|^''([^'']+)''|^(\S+)') {
        foreach ($group in 1..3) {
            if ($Matches.ContainsKey($group) -and $Matches[$group]) {
                $compiler = $Matches[$group]
                break
            }
        }
    }
    if (-not (Get-Command -Name $compiler -CommandType Application -ErrorAction SilentlyContinue)) {
        return "the configured C compiler '$($settings.CC)' was not found on PATH"
    }
    return $null
}
