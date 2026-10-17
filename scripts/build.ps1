<#
.SYNOPSIS
Cross-compile netdiag for Windows and macOS (amd64 + arm64) and zip each
target with README.md, HOW-TO-OPEN.txt and the license files into dist/.

.EXAMPLE
pwsh scripts/build.ps1                  # version defaults to git describe
pwsh scripts/build.ps1 -Version v1.2.0
#>
[CmdletBinding()]
param(
    [string]$Version = $env:VERSION
)
$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

if (-not $Version) {
    $Version = (git describe --tags --always --dirty 2>$null)
    if ($LASTEXITCODE -ne 0 -or -not $Version) { $Version = 'dev' }
}
$Dist = 'dist'
$Targets = 'windows/amd64', 'windows/arm64', 'darwin/amd64', 'darwin/arm64'

$Docs = 'README.md', 'HOW-TO-OPEN.txt', 'LICENSE', 'NOTICE', 'THIRD_PARTY_LICENSES.txt'
foreach ($f in $Docs) {
    if (-not (Test-Path $f -PathType Leaf)) { throw "missing $f" }
}

function Invoke-Native {
    param([string]$Exe, [string[]]$Arguments)
    & $Exe @Arguments
    if ($LASTEXITCODE -ne 0) { throw "$Exe $($Arguments -join ' ') failed (exit $LASTEXITCODE)" }
}

$stage = Join-Path ([IO.Path]::GetTempPath()) ("netdiag-build-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $stage | Out-Null
$saved = @{ CGO_ENABLED = $env:CGO_ENABLED; GOOS = $env:GOOS; GOARCH = $env:GOARCH }
try {
    New-Item -ItemType Directory -Force -Path $Dist | Out-Null

    # The license file must list exactly the modules being shipped.
    $env:CGO_ENABLED = $null; $env:GOOS = $null; $env:GOARCH = $null
    Invoke-Native go @('run', './tools/licenses', '-check')

    # tools/mkzip (pure Go) puts files at the zip root and keeps the netdiag
    # binary executable for macOS, matching scripts/build.sh exactly.
    $mkzip = Join-Path $stage 'mkzip.exe'
    Invoke-Native go @('build', '-o', $mkzip, './tools/mkzip')
    Write-Host "netdiag version: $Version"

    # Version info, manifest and icon for netdiag.exe (cmd/netdiag/winres). go
    # build links the generated rsrc_windows_*.syso into the Windows targets only.
    Invoke-Native go @('tool', 'go-winres', 'make', '--in', 'cmd/netdiag/winres/winres.json',
        '--out', 'cmd/netdiag/rsrc', '--arch', 'amd64,arm64',
        '--product-version', $Version, '--file-version', $Version)

    foreach ($target in $Targets) {
        $goos, $goarch = $target -split '/'
        $bin = if ($goos -eq 'windows') { 'netdiag.exe' } else { 'netdiag' }

        $pkg = Join-Path $stage "$goos-$goarch"
        New-Item -ItemType Directory -Path $pkg | Out-Null
        Write-Host "building $goos/$goarch"
        $env:CGO_ENABLED = '0'; $env:GOOS = $goos; $env:GOARCH = $goarch
        Invoke-Native go @('build', '-trimpath', '-ldflags', "-s -w -X main.version=$Version",
            '-o', (Join-Path $pkg $bin), './cmd/netdiag')
        $env:CGO_ENABLED = $null; $env:GOOS = $null; $env:GOARCH = $null
        Copy-Item $Docs -Destination $pkg

        $zip = Join-Path $Dist "netdiag-$goos-$goarch.zip"
        Remove-Item -Force -ErrorAction SilentlyContinue $zip
        $files = Get-ChildItem -File $pkg | ForEach-Object FullName
        Invoke-Native $mkzip (@('-o', $zip, '-x', 'netdiag', '-x', 'netdiag.exe') + $files)
        Write-Host "wrote $zip"
        Remove-Item -Recurse -Force $pkg
    }
}
finally {
    foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $stage
    Remove-Item -Force -ErrorAction SilentlyContinue cmd/netdiag/rsrc_windows_*.syso
}
