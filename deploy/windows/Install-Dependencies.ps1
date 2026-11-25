#Requires -Version 5.1
#Requires -RunAsAdministrator
<#
.SYNOPSIS
Installs native Garnet and RustFS services for a single-machine Windows POC.
.DESCRIPTION
Supply dedicated directories containing the extracted Garnet 2.2.14 net10.0
Windows x64 distribution and RustFS 1.0.4 Windows x64 distribution, the official
WinSW 2.14.0 x64 executable, and two files containing the RustFS credentials.
Install the x64 .NET 10 runtime first. Keep Common.ps1 beside this script.
PostgreSQL is installed separately with its official Windows installer.
curl.exe must support AWS SigV4; the script creates and verifies the omnara bucket.

This replaces ownership and ACLs on the supplied program directories. Do not use
shared software directories. DataDirectory must be empty and on local NTFS, with
no spaces in its path (a RustFS 1.0.4 argument-parser limitation). The services
listen only on loopback; this script changes no firewall rules. RustFS's Windows
build is intended for development/debugging; this setup is for a POC.

The Redis password is generated and stored privately, never printed. RustFS
credential files are copied into private configuration. A failed installation
leaves its files and services for diagnosis; existing installations are refused.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string] $GarnetDirectory,
    [Parameter(Mandatory = $true)][string] $RustFSDirectory,
    [Parameter(Mandatory = $true)][string] $RustFSAccessKeyFile,
    [Parameter(Mandatory = $true)][string] $RustFSSecretKeyFile,
    [Parameter(Mandatory = $true)][string] $WinSWPath,
    [string] $DataDirectory = 'C:\ProgramData\Omnara\dependencies'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Common.ps1')

function Test-GarnetReady([string] $Password) {
    $client = New-Object Net.Sockets.TcpClient
    try {
        if (-not $client.ConnectAsync('127.0.0.1', 6379).Wait(3000)) { return $false }
        $stream = $client.GetStream()
        $stream.ReadTimeout = 3000
        $stream.WriteTimeout = 3000
        $reader = New-Object IO.StreamReader($stream, [Text.Encoding]::UTF8)
        # Check authentication is enforced before checking the supplied password.
        $commands = @(@('PING'), @('AUTH', $Password), @('PING'))
        $expected = @('-NOAUTH', '+OK', '+PONG')
        for ($index = 0; $index -lt $commands.Count; $index++) {
            $command = $commands[$index]
            $request = '*' + $command.Count + "`r`n"
            foreach ($argument in $command) {
                $request += '$' + [Text.Encoding]::UTF8.GetByteCount($argument) + "`r`n" + $argument + "`r`n"
            }
            $bytes = [Text.Encoding]::UTF8.GetBytes($request)
            $stream.Write($bytes, 0, $bytes.Length)
            $response = $reader.ReadLine()
            if (-not $response) { return $false }
            if ($index -eq 0) {
                if (-not $response.StartsWith('-NOAUTH ')) { return $false }
            } elseif ($response -ne $expected[$index]) { return $false }
        }
        return $true
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

if (-not [Environment]::Is64BitProcess) { throw 'Run this script in 64-bit Windows PowerShell.' }
$GarnetDirectory = Resolve-LocalPath $GarnetDirectory
$RustFSDirectory = Resolve-LocalPath $RustFSDirectory
$RustFSAccessKeyFile = Resolve-LocalPath $RustFSAccessKeyFile
$RustFSSecretKeyFile = Resolve-LocalPath $RustFSSecretKeyFile
$WinSWPath = Resolve-LocalPath $WinSWPath
$DataDirectory = Resolve-LocalPath $DataDirectory
if ($DataDirectory -match '\s') { throw 'RustFS 1.0.4 requires a data directory path without spaces.' }
$programDirectories = [ordered]@{ garnet = $GarnetDirectory; rustfs = $RustFSDirectory }
$roles = @('garnet', 'rustfs')
$accounts = @($roles | ForEach-Object { 'NT SERVICE\omnara-' + $_ })
$directories = @($GarnetDirectory, $RustFSDirectory, $DataDirectory)
for ($i = 0; $i -lt $directories.Count; $i++) {
    for ($j = $i + 1; $j -lt $directories.Count; $j++) {
        $left = $directories[$i]
        $right = $directories[$j]
        if ($left.Equals($right, [StringComparison]::OrdinalIgnoreCase) -or
            $left.StartsWith($right + '\', [StringComparison]::OrdinalIgnoreCase) -or
            $right.StartsWith($left + '\', [StringComparison]::OrdinalIgnoreCase)) {
            throw 'Program and data directories must be separate, with none containing another.'
        }
    }
}
foreach ($directory in $programDirectories.Values) {
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { throw "Missing program directory: $directory" }
    if (Get-ChildItem -LiteralPath $directory -Recurse -Force |
        Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint } | Select-Object -First 1) {
        throw "Program directories must not contain reparse points: $directory"
    }
}
foreach ($executable in @((Join-Path $GarnetDirectory 'GarnetServer.exe'), (Join-Path $RustFSDirectory 'rustfs.exe'))) {
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf)) { throw "Missing executable: $executable" }
}
$dotnet = Join-Path $env:ProgramFiles 'dotnet\dotnet.exe'
if (-not (Test-Path -LiteralPath $dotnet)) {
    throw 'Install the x64 .NET 10 runtime required by Garnet before continuing.'
}
$runtimes = & $dotnet --list-runtimes
if ($LASTEXITCODE -ne 0 -or -not ($runtimes | Select-String '^Microsoft.NETCore.App 10\.')) {
    throw 'Install the x64 .NET 10 runtime required by Garnet before continuing.'
}
if ((Get-FileHash -LiteralPath $WinSWPath -Algorithm SHA256).Hash -ne $WinSWHash) {
    throw 'WinSW SHA256 mismatch; supply the official v2.14.0 x64 executable.'
}
foreach ($path in @($RustFSAccessKeyFile, $RustFSSecretKeyFile)) {
    $credential = [IO.File]::ReadAllText($path).Trim()
    if (-not $credential -or $credential -match '[\r\n]') { throw 'RustFS credential files must contain one nonempty value.' }
}
if ([IO.File]::ReadAllText($RustFSAccessKeyFile).Contains(':')) { throw 'The RustFS access key must not contain a colon.' }
$curl = (Get-Command curl.exe -CommandType Application -ErrorAction Stop).Source
$curlHelp = & $curl --help all
if ($LASTEXITCODE -ne 0 -or -not ($curlHelp | Select-String -SimpleMatch '--aws-sigv4')) {
    throw 'Update curl.exe to a version supporting --aws-sigv4 before installing.'
}
foreach ($role in $roles) {
    if (Get-Service -Name "omnara-$role" -ErrorAction SilentlyContinue) {
        throw "Service omnara-$role already exists; this script only performs initial installations."
    }
}
if ((Test-Path -LiteralPath $DataDirectory) -and @(Get-ChildItem -LiteralPath $DataDirectory -Force).Count -gt 0) {
    throw 'DataDirectory must be empty; existing dependency data will not be changed.'
}
$drive = New-Object IO.DriveInfo([IO.Path]::GetPathRoot($DataDirectory))
if ($drive.DriveFormat -ne 'NTFS' -or $drive.DriveType -ne 'Fixed') { throw 'DataDirectory must be on local fixed NTFS.' }
foreach ($port in @(6379, 9000)) {
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) {
        throw "Port $port is already in use; existing listeners will not be changed."
    }
}

[void] (New-Item -ItemType Directory -Path $DataDirectory -Force)
Set-PrivatePermissions $DataDirectory
foreach ($role in $roles) {
    $root = Join-Path $DataDirectory $role
    foreach ($child in @('config', 'logs', 'temp', 'data')) {
        [void] (New-Item -ItemType Directory -Path (Join-Path $root $child) -Force)
    }
}
$random = New-Object byte[] 32
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($random) } finally { $rng.Dispose() }
$password = [BitConverter]::ToString($random).Replace('-', '').ToLowerInvariant()
$passwordFile = Join-Path $DataDirectory 'garnet\config\password.txt'
[IO.File]::WriteAllText($passwordFile, $password)
$garnet = [ordered]@{
    Address = '127.0.0.1'; Port = 6379; LogMemorySize = '256m'; PageSize = '1m'
    IndexMemorySize = '16m'; SegmentSize = '64m'; DisableObjects = $true
    EnableLua = $true; LuaTransactionMode = $true; EnableStorageTier = $true; EnableScatterGatherGet = $false
    LogDir = (Join-Path $DataDirectory 'garnet\data\log')
    CheckpointDir = (Join-Path $DataDirectory 'garnet\data\checkpoint')
    EnableAOF = $true; AofMemorySize = '16m'; AofPageSize = '2m'; WaitForCommit = $true; CommitFrequencyMs = 0
    Recover = $true; FailOnRecoveryError = $true; CheckpointFrequencySecs = 300
    CompactionType = 'Lookup'; CompactionMaxSegments = 8
    AuthenticationMode = 'Password'; Password = $password
}
# Disk tiering prevents live keys being evicted when the bounded log fills; Lua
# transaction mode preserves the atomic operations Omnara's Redis scripts need.
[IO.File]::WriteAllText((Join-Path $DataDirectory 'garnet\config\garnet.json'), ($garnet | ConvertTo-Json))
Copy-Item -LiteralPath $RustFSAccessKeyFile -Destination (Join-Path $DataDirectory 'rustfs\config\access-key.txt')
Copy-Item -LiteralPath $RustFSSecretKeyFile -Destination (Join-Path $DataDirectory 'rustfs\config\secret-key.txt')
Set-PrivatePermissions $RustFSAccessKeyFile
Set-PrivatePermissions $RustFSSecretKeyFile

foreach ($role in $roles) {
    $root = Join-Path $DataDirectory $role
    $id = "omnara-$role"
    $wrapper = Join-Path $root "$id.exe"
    Copy-Item -LiteralPath $WinSWPath -Destination $wrapper
    if ($role -eq 'garnet') {
        $executable = Join-Path $GarnetDirectory 'GarnetServer.exe'
        $arguments = '--config-import-path "' + $root + '\config\garnet.json"'
    } else {
        $executable = Join-Path $RustFSDirectory 'rustfs.exe'
        $arguments = 'server --address 127.0.0.1:9000 --console-address 127.0.0.1:9001 "' + $root + '\data"'
    }
    $document = New-Object Xml.XmlDocument
    $node = $document.CreateElement('service')
    [void] $document.AppendChild($node)
    foreach ($entry in ([ordered]@{
        id = $id; name = "Omnara $role"; description = "Omnara native $role dependency"
        executable = $executable; arguments = $arguments; workingdirectory = $programDirectories[$role]
    }).GetEnumerator()) { [void] (Add-XmlElement $node $entry.Key $entry.Value) }
    Add-ServiceDefaults $node $id (Join-Path $root 'logs')
    $environment = [ordered]@{ TEMP = (Join-Path $root 'temp'); TMP = (Join-Path $root 'temp') }
    if ($role -eq 'rustfs') {
        $environment['RUSTFS_ACCESS_KEY_FILE'] = Join-Path $root 'config\access-key.txt'
        $environment['RUSTFS_SECRET_KEY_FILE'] = Join-Path $root 'config\secret-key.txt'
        $environment['RUSTFS_CONSOLE_ENABLE'] = 'false'
    }
    foreach ($entry in $environment.GetEnumerator()) {
        $variable = Add-XmlElement $node 'env' ''
        $variable.SetAttribute('name', $entry.Key)
        $variable.SetAttribute('value', $entry.Value)
    }
    $document.Save((Join-Path $root "$id.xml"))
    & $wrapper install | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Installing $id failed; inspect $root\logs." }
}

Set-PrivatePermissions $DataDirectory $accounts
foreach ($role in $roles) {
    $root = Join-Path $DataDirectory $role
    $account = @("NT SERVICE\omnara-$role")
    Set-PrivatePermissions $root $account
    Set-PrivatePermissions (Join-Path $root 'config') $account
    foreach ($child in @('logs', 'temp', 'data')) {
        Set-PrivatePermissions (Join-Path $root $child) $account 'Modify'
    }
    Protect-ProgramDirectory $programDirectories[$role] $account
    Start-Service -Name "omnara-$role"
}
foreach ($role in $roles) {
    $deadline = [DateTime]::UtcNow.AddSeconds(90)
    $ready = $false
    do {
        if ((Get-Service -Name "omnara-$role").Status -eq 'Running') {
            if ($role -eq 'garnet') {
                $ready = Test-GarnetReady $password
            } else {
                try {
                    $response = Invoke-WebRequest -UseBasicParsing -Uri 'http://127.0.0.1:9000/health/ready' -TimeoutSec 3
                    $ready = $response.StatusCode -eq 200
                } catch { $ready = $false }
            }
        }
        if (-not $ready) { Start-Sleep -Seconds 1 }
    } while (-not $ready -and [DateTime]::UtcNow -lt $deadline)
    if (-not $ready) { throw "omnara-$role did not become ready; inspect $DataDirectory\$role\logs." }
}

# Use the same S3 SigV4 provisioning as compose. Pass credentials through stdin,
# never the process command line; discard response bodies and private diagnostics.
$credentials = [IO.File]::ReadAllText($RustFSAccessKeyFile).Trim() + ':' + [IO.File]::ReadAllText($RustFSSecretKeyFile).Trim()
$credentials = $credentials.Replace('\', '\\').Replace('"', '\"')
$curlConfig = @"
url = "http://127.0.0.1:9000/omnara"
noproxy = "*"
aws-sigv4 = "aws:amz:us-east-1:s3"
user = "$credentials"
request = "PUT"
output = "NUL"
write-out = "%{http_code}"
silent
connect-timeout = 5
max-time = 15
"@
$configBytes = [Text.Encoding]::UTF8.GetBytes($curlConfig + "`n")
$start = New-Object Diagnostics.ProcessStartInfo
$start.FileName = $curl
$start.Arguments = '--disable --config -'
$start.UseShellExecute = $false
$start.RedirectStandardInput = $true
$start.RedirectStandardOutput = $true
$start.RedirectStandardError = $true
$process = [Diagnostics.Process]::Start($start)
try {
    $stdout = $process.StandardOutput.ReadToEndAsync()
    $stderr = $process.StandardError.ReadToEndAsync()
    # The .NET Framework text writer uses the console's legacy encoding.
    $process.StandardInput.BaseStream.Write($configBytes, 0, $configBytes.Length)
    $process.StandardInput.BaseStream.Close()
    $process.WaitForExit()
    if ($process.ExitCode -ne 0 -or $stdout.Result -notmatch '^20[04]$') {
        throw 'RustFS bucket creation failed; inspect the protected RustFS logs. Application services have not been installed.'
    }
} finally {
    $process.Dispose()
}
Write-Host 'Garnet and RustFS services are running and ready on loopback ports 6379 and 9000.'
Write-Host "Redis password for the application environment: $passwordFile (hex; safe to insert directly into OMNARA_REDIS_URL)."
Write-Host "RustFS credentials: $DataDirectory\rustfs\config. The omnara bucket is ready."
