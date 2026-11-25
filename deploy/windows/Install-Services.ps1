#Requires -Version 5.1
#Requires -RunAsAdministrator
<#
.SYNOPSIS
Installs the three Omnara control-plane services on a single Windows Server.
.DESCRIPTION
Run once after provisioning PostgreSQL, Redis/Garnet, and S3 storage. Supply a
dedicated binary directory containing omnara-{api,worker,maintenance,migrate}.exe,
omnara-sandbox.exe, migrations, and the generated mcp-registry.json snapshot; and a
dedicated file-tools directory containing rg.exe, GNU sed.exe, and its runtime DLLs.
The API must include the embedded web UI. Keep Common.ps1 beside this script.

The supplied binary/tool directories have their ACLs replaced: Administrators and
SYSTEM retain full control; service accounts receive read/execute only. Do not
point them at shared software directories. DataDirectory must be absent or empty.
The environment file is copied into DataDirectory; edit that protected copy for
subsequent configuration changes, then restart the services.

This installs no dependencies, IIS configuration, or firewall rules. A failed
installation leaves its files and services available for inspection; it does not
roll back database migrations. Existing services are never replaced automatically.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $BinaryDirectory,
    [Parameter(Mandatory = $true)][string] $EnvironmentFile,
    [Parameter(Mandatory = $true)][string] $FileToolsDirectory,
    [string] $DataDirectory = 'C:\ProgramData\Omnara\control-plane',
    [Parameter(Mandatory = $true)][string] $WinSWPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$services = @('api', 'worker', 'maintenance')
$accounts = @($services | ForEach-Object { 'NT SERVICE\omnara-' + $_ })

. (Join-Path $PSScriptRoot 'Common.ps1')

$BinaryDirectory = Resolve-LocalPath $BinaryDirectory
$FileToolsDirectory = Resolve-LocalPath $FileToolsDirectory
$EnvironmentFile = Resolve-LocalPath $EnvironmentFile
$DataDirectory = Resolve-LocalPath $DataDirectory
$programDirectories = @($BinaryDirectory, $FileToolsDirectory)
if ($BinaryDirectory.Equals($FileToolsDirectory, [StringComparison]::OrdinalIgnoreCase) -or
    $BinaryDirectory.StartsWith($FileToolsDirectory + '\', [StringComparison]::OrdinalIgnoreCase) -or
    $FileToolsDirectory.StartsWith($BinaryDirectory + '\', [StringComparison]::OrdinalIgnoreCase)) {
    throw 'BinaryDirectory and FileToolsDirectory must be separate, with neither containing the other.'
}
foreach ($directory in $programDirectories) {
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) {
        throw "Missing program directory: $directory"
    }
    if ($DataDirectory.Equals($directory, [StringComparison]::OrdinalIgnoreCase) -or
        $DataDirectory.StartsWith($directory + '\', [StringComparison]::OrdinalIgnoreCase) -or
        $directory.StartsWith($DataDirectory + '\', [StringComparison]::OrdinalIgnoreCase)) {
        throw 'DataDirectory must be separate from the binary and file-tools directories.'
    }
    if (Get-ChildItem -LiteralPath $directory -Force -Recurse |
        Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint } |
        Select-Object -First 1) {
        throw "Program directories must not contain reparse points: $directory"
    }
}
foreach ($name in @('api', 'worker', 'maintenance', 'migrate', 'sandbox')) {
    if (-not (Test-Path -LiteralPath (Join-Path $BinaryDirectory "omnara-$name.exe") -PathType Leaf)) {
        throw "Missing omnara-$name.exe in BinaryDirectory."
    }
}
foreach ($name in @('rg.exe', 'sed.exe')) {
    if (-not (Test-Path -LiteralPath (Join-Path $FileToolsDirectory $name) -PathType Leaf)) {
        throw "Missing $name in FileToolsDirectory."
    }
}
$migrationsDirectory = Join-Path $BinaryDirectory 'migrations'
if (-not (Test-Path -LiteralPath $migrationsDirectory -PathType Container)) {
    throw 'BinaryDirectory must contain the migrations directory from this release.'
}
$registrySnapshot = Join-Path $BinaryDirectory 'mcp-registry.json'
if (-not (Test-Path -LiteralPath $registrySnapshot -PathType Leaf)) {
    throw 'BinaryDirectory must contain the generated mcp-registry.json snapshot.'
}
foreach ($service in $services) {
    if (Get-Service -Name "omnara-$service" -ErrorAction SilentlyContinue) {
        throw "Service omnara-$service already exists. This script only performs initial installations."
    }
}
if ((Test-Path -LiteralPath $DataDirectory) -and
    @((Get-ChildItem -LiteralPath $DataDirectory -Force)).Count -ne 0) {
    throw 'DataDirectory must be empty. Imported memory directories need a separately reviewed migration.'
}
$drive = New-Object IO.DriveInfo([IO.Path]::GetPathRoot($DataDirectory))
if ($drive.DriveFormat -ne 'NTFS' -or $drive.DriveType -ne 'Fixed') {
    throw 'DataDirectory must be on a local fixed NTFS volume.'
}

# Match WinSW v2 envFile parsing: literal, unquoted KEY=VALUE; no shell expansion.
$environment = @{}
foreach ($line in [IO.File]::ReadAllLines($EnvironmentFile)) {
    if ($line.Length -eq 0 -or $line.StartsWith('#')) { continue }
    $separator = $line.IndexOf('=')
    if ($separator -lt 1) { throw 'EnvironmentFile contains an invalid KEY=VALUE line.' }
    $name = $line.Substring(0, $separator).Trim()
    if ($name -notmatch '^[A-Z][A-Z0-9_]*$' -or $environment.ContainsKey($name)) {
        throw 'EnvironmentFile contains an invalid or duplicate variable name.'
    }
    $environment[$name] = $line.Substring($separator + 1).Trim()
}
foreach ($name in @('OMNARA_DATABASE_URL', 'OMNARA_REDIS_URL', 'OMNARA_PUBLIC_URL', 'OMNARA_BLOB_S3_BUCKET',
    'OMNARA_SECRET_ENCRYPTION_KEYS', 'OMNARA_SECRET_ENCRYPTION_ACTIVE_KEY_ID', 'OMNARA_SKILL_DOWNLOAD_SIGNING_KEY')) {
    if (-not $environment[$name] -or $environment[$name].Contains('REPLACE_')) {
        throw "Set $name in EnvironmentFile before installing."
    }
}
foreach ($value in $environment.Values) {
    if ($value.Contains('REPLACE_')) { throw 'Replace all REPLACE_ placeholders in EnvironmentFile before installing.' }
}
$WinSWPath = Resolve-LocalPath $WinSWPath
if ((Get-FileHash -LiteralPath $WinSWPath -Algorithm SHA256).Hash -ne $WinSWHash) {
    throw 'WinSW SHA256 mismatch; only the pinned official v2.14.0 x64 executable is accepted.'
}

[void] (New-Item -ItemType Directory -Path $DataDirectory -Force)
Set-PrivatePermissions $DataDirectory
$configDirectory = Join-Path $DataDirectory 'config'
$wrapperDirectory = Join-Path $DataDirectory 'services'
$memoryDirectory = Join-Path $DataDirectory 'memory'
foreach ($directory in @($configDirectory, $wrapperDirectory, $memoryDirectory)) {
    [void] (New-Item -ItemType Directory -Path $directory)
}
$installedEnvironment = Join-Path $configDirectory 'control-plane.env'
Copy-Item -LiteralPath $EnvironmentFile -Destination $installedEnvironment
# Protect the supplied file too; it still contains credentials after copying.
Set-PrivatePermissions $EnvironmentFile
& fsutil.exe file setCaseSensitiveInfo $memoryDirectory enable | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Could not enable case sensitivity on the empty memory directory.' }

$overrides = [ordered]@{
    OMNARA_API_ADDR = '127.0.0.1:8080'
    OMNARA_API_METRICS_ADDR = '127.0.0.1:8081'
    OMNARA_WORKER_METRICS_ADDR = '127.0.0.1:8082'
    OMNARA_MAINTENANCE_METRICS_ADDR = '127.0.0.1:8083'
    OMNARA_WEB_SERVING = 'embedded'
    OMNARA_MEMORY_DIR = $memoryDirectory
    OMNARA_MIGRATIONS_DIR = $migrationsDirectory
    OMNARA_MCP_REGISTRY_SNAPSHOT_PATH = $registrySnapshot
}

# Migrate once as the installing administrator, before any service can claim work.
foreach ($directory in $programDirectories) { Protect-ProgramDirectory $directory @() }
$migrationLog = Join-Path $configDirectory 'migration.log'
$start = New-Object Diagnostics.ProcessStartInfo
$start.FileName = Join-Path $BinaryDirectory 'omnara-migrate.exe'
$start.WorkingDirectory = $BinaryDirectory
$start.UseShellExecute = $false
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
foreach ($entry in $environment.GetEnumerator()) { $start.EnvironmentVariables[$entry.Key] = $entry.Value }
foreach ($entry in $overrides.GetEnumerator()) { $start.EnvironmentVariables[$entry.Key] = $entry.Value }
$process = [Diagnostics.Process]::Start($start)
try {
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    $process.WaitForExit()
    [IO.File]::WriteAllText($migrationLog, $stdout.Result + $stderr.Result)
    if ($process.ExitCode -ne 0) {
        throw "Database migration failed. Inspect $migrationLog. No services were installed and migrations were not rolled back. Fix the cause, move aside the newly created $DataDirectory, and retry with an empty DataDirectory."
    }
} finally {
    $process.Dispose()
}

foreach ($service in $services) {
    $id = "omnara-$service"
    $privateDirectory = Join-Path $DataDirectory $service
    $tempDirectory = Join-Path $privateDirectory 'temp'
    $logDirectory = Join-Path $privateDirectory 'logs'
    [void] (New-Item -ItemType Directory -Path $tempDirectory -Force)
    [void] (New-Item -ItemType Directory -Path $logDirectory)
    $wrapper = Join-Path $wrapperDirectory "$id.exe"
    Copy-Item -LiteralPath $WinSWPath -Destination $wrapper
    $document = New-Object Xml.XmlDocument
    $root = $document.CreateElement('service')
    [void] $document.AppendChild($root)
    foreach ($entry in ([ordered]@{
        id = $id; name = "Omnara $service"; description = "Omnara control-plane $service"
        executable = (Join-Path $BinaryDirectory "$id.exe")
        workingdirectory = $BinaryDirectory; envFile = $installedEnvironment
    }).GetEnumerator()) { [void] (Add-XmlElement $root $entry.Key $entry.Value) }
    Add-ServiceDefaults $root $id $logDirectory
    $serviceEnvironment = [ordered]@{
        PATH = "$BinaryDirectory;%SystemRoot%\System32;%SystemRoot%"
        TEMP = $tempDirectory; TMP = $tempDirectory
    }
    if ($service -eq 'worker') { $serviceEnvironment['PATH'] = "$FileToolsDirectory;" + $serviceEnvironment['PATH'] }
    foreach ($entry in $overrides.GetEnumerator()) { $serviceEnvironment[$entry.Key] = $entry.Value }
    foreach ($entry in $serviceEnvironment.GetEnumerator()) {
        $node = Add-XmlElement $root 'env' ''
        $node.SetAttribute('name', $entry.Key)
        $node.SetAttribute('value', $entry.Value)
    }
    $document.Save((Join-Path $wrapperDirectory "$id.xml"))
    & $wrapper install | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Installing $id failed. Inspect its wrapper log in $logDirectory" }
}

# Virtual account SIDs become resolvable after installation, before first start.
Set-PrivatePermissions $DataDirectory $accounts
Set-PrivatePermissions $configDirectory $accounts 'ReadAndExecute'
Set-PrivatePermissions $installedEnvironment $accounts 'Read'
Set-PrivatePermissions $migrationLog
Set-PrivatePermissions $wrapperDirectory $accounts
Set-PrivatePermissions $memoryDirectory $accounts 'Modify'
Protect-ProgramDirectory $BinaryDirectory $accounts
Protect-ProgramDirectory $FileToolsDirectory @('NT SERVICE\omnara-worker')
foreach ($service in $services) {
    Set-PrivatePermissions (Join-Path $DataDirectory $service) @("NT SERVICE\omnara-$service") 'Modify'
}
foreach ($service in $services) {
    Start-Service -Name "omnara-$service"
}

foreach ($index in 0..($services.Count - 1)) {
    $id = 'omnara-' + $services[$index]
    $url = 'http://127.0.0.1:' + (8081 + $index) + '/readyz'
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    $ready = $false
    do {
        if ((Get-Service -Name $id).Status -eq 'Running') {
            try {
                $response = Invoke-RestMethod -Uri $url -TimeoutSec 3
                $ready = $response.status -eq 'ready'
            } catch { $ready = $false }
        }
        if (-not $ready) { Start-Sleep -Seconds 1 }
    } while (-not $ready -and [DateTime]::UtcNow -lt $deadline)
    if (-not $ready) {
        throw "$id did not become ready. Inspect its protected logs under $DataDirectory\$($services[$index])\logs. Correct $installedEnvironment and restart the installed services with Restart-Service omnara-api, omnara-worker, omnara-maintenance; do not reinstall them."
    }
}
if (Get-ChildItem -LiteralPath (Join-Path $DataDirectory 'worker\logs') -Filter '*.log' |
    Select-String -SimpleMatch 'worker.file_tools_unavailable') {
    Write-Warning 'One or more worker file tools failed startup checks; inspect the protected worker logs. An edit-probe failure disables scripted edits until omnara-worker restarts. Searches check availability on each call.'
}
Write-Host "Omnara services are running and ready. Configuration: $installedEnvironment"
Write-Host 'The dashboard and API listen on 127.0.0.1:8080 for an IIS reverse proxy.'
