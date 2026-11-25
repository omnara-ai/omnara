#Requires -Version 5.1
[CmdletBinding()]
param([string] $OutputDirectory = (Join-Path $PSScriptRoot '..\..\bin\windows'))

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
if ((Test-Path -LiteralPath $OutputDirectory) -and @(Get-ChildItem -LiteralPath $OutputDirectory -Force).Count -gt 0) {
    throw 'OutputDirectory must be empty to avoid mixing release artifacts.'
}
[void] (Get-Command go -CommandType Application -ErrorAction Stop)
[void] (Get-Command corepack.cmd -CommandType Application -ErrorAction Stop)
[void] (New-Item -ItemType Directory -Path $OutputDirectory -Force)
$originalEnvironment = @{}
foreach ($name in @('CGO_ENABLED', 'GOOS', 'GOARCH')) { $originalEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
Push-Location (Join-Path $PSScriptRoot '..\..')
try {
    Push-Location frontend
    try {
        & corepack.cmd pnpm install --frozen-lockfile
        if ($LASTEXITCODE -ne 0) { throw 'Frontend dependency installation failed.' }
        foreach ($command in @('generate:api', 'typecheck', 'build')) {
            & corepack.cmd pnpm run $command
            if ($LASTEXITCODE -ne 0) { throw "Frontend $command failed." }
        }
    } finally { Pop-Location }

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    foreach ($name in @('api', 'worker', 'maintenance', 'migrate', 'sandbox')) {
        $arguments = @('build', '-trimpath')
        if ($name -eq 'api') { $arguments += @('-tags', 'requirespa') }
        $arguments += @('-o', (Join-Path $OutputDirectory "omnara-$name.exe"), "./cmd/$name")
        & go @arguments
        if ($LASTEXITCODE -ne 0) { throw "Building omnara-$name.exe failed." }
    }
    Copy-Item -LiteralPath migrations -Destination (Join-Path $OutputDirectory 'migrations') -Recurse
    & go run ./tools/mcp-registry-sync -out (Join-Path $OutputDirectory 'mcp-registry.json')
    if ($LASTEXITCODE -ne 0) { throw 'MCP registry snapshot generation failed.' }
} finally {
    foreach ($name in $originalEnvironment.Keys) { [Environment]::SetEnvironmentVariable($name, $originalEnvironment[$name], 'Process') }
    Pop-Location
}
Write-Host "Windows control-plane artifacts are ready in $OutputDirectory"
