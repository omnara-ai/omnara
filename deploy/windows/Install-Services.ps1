#Requires -Version 5.1
#Requires -RunAsAdministrator

<#
.DESCRIPTION
BinaryDirectory and FileToolsDirectory must be dedicated directories: their
permissions are replaced. DataDirectory must be absent or empty. EnvironmentFile
is copied there; edit the protected copy for subsequent configuration changes.
A failed installation retains files and services for inspection and does not
roll back database migrations. Existing services are never replaced.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $BinaryDirectory,
    [Parameter(Mandatory = $true)][string] $EnvironmentFile,
    [Parameter(Mandatory = $true)][string] $FileToolsDirectory,
    [string] $DataDirectory = 'C:\ProgramData\Omnara\control-plane',
    [Parameter(Mandatory = $true)][string] $WinSWPath,
    [ValidateRange(1, 65535)][int] $ApiPort = 8080,
    [ValidateRange(1, 65535)][int] $ApiMetricsPort = 8081,
    [ValidateRange(1, 65535)][int] $WorkerMetricsPort = 8082,
    [ValidateRange(1, 65535)][int] $MaintenanceMetricsPort = 8083
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$metricsPorts = [ordered]@{ api = $ApiMetricsPort; worker = $WorkerMetricsPort; maintenance = $MaintenanceMetricsPort }
$services = @($metricsPorts.Keys)
$listenPorts = @($ApiPort) + @($metricsPorts.Values)
if (@($listenPorts | Sort-Object -Unique).Count -ne 4) {
    throw 'ApiPort, ApiMetricsPort, WorkerMetricsPort, and MaintenanceMetricsPort must be distinct.'
}
foreach ($service in $services) {
    if (Get-Service -Name "omnara-$service" -ErrorAction SilentlyContinue) {
        throw "Service omnara-$service already exists. This script only performs initial installations."
    }
}
$occupiedPorts = @(Get-NetTCPConnection -LocalPort $listenPorts -State Listen -ErrorAction SilentlyContinue)
if ($occupiedPorts.Count) {
    $ports = ($occupiedPorts.LocalPort | Sort-Object -Unique) -join ', '
    throw "Required Omnara ports are already in use: $ports. Free them or choose different -ApiPort, -ApiMetricsPort, -WorkerMetricsPort, and -MaintenanceMetricsPort values; EnvironmentFile does not set these listeners."
}
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
    OMNARA_API_ADDR = "127.0.0.1:$ApiPort"
    OMNARA_API_METRICS_ADDR = "127.0.0.1:$($metricsPorts['api'])"
    OMNARA_WORKER_METRICS_ADDR = "127.0.0.1:$($metricsPorts['worker'])"
    OMNARA_MAINTENANCE_METRICS_ADDR = "127.0.0.1:$($metricsPorts['maintenance'])"
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

foreach ($service in $services) {
    $id = 'omnara-' + $service
    $url = "http://127.0.0.1:$($metricsPorts[$service])/readyz"
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
        throw "$id did not become ready. Inspect its protected logs under $DataDirectory\$service\logs and fix the reported cause. For configuration or dependency errors, correct $installedEnvironment; for listener conflicts, free the selected ports $($listenPorts -join ', '). Then restart the installed services with Restart-Service omnara-api, omnara-worker, omnara-maintenance; do not reinstall them."
    }
}
if (Get-ChildItem -LiteralPath (Join-Path $DataDirectory 'worker\logs') -Filter '*.log' |
    Select-String -SimpleMatch 'worker.file_tools_unavailable') {
    Write-Warning 'One or more worker file tools failed startup checks; inspect the protected worker logs and fix the file tools. Searches and scripted edits check the tools on each call, so no restart is needed.'
}
Write-Host "Omnara services are running and ready. Configuration: $installedEnvironment"
Write-Host "The dashboard and API listen on 127.0.0.1:$ApiPort. For IIS, use Configure-IIS.ps1 -BackendPort $ApiPort."
Write-Host "Loopback metrics ports: API $ApiMetricsPort, worker $WorkerMetricsPort, maintenance $MaintenanceMetricsPort."
