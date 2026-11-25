#Requires -Version 5.1
#Requires -RunAsAdministrator
[CmdletBinding()]
param(
    [string] $SiteDirectory = 'C:\ProgramData\Omnara\iis',
    [ValidateRange(1, 65535)][int] $Port = 8088,
    [ValidateRange(1, 65535)][int] $BackendPort = 8080
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if ($Port -eq $BackendPort) { throw 'Port and BackendPort must be different.' }
if (Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue) {
    throw "IIS port $Port is already in use. Choose a free -Port distinct from the Omnara service ports."
}
$listener = @(Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $BackendPort -State Listen -ErrorAction SilentlyContinue)
if ($listener.Count -ne 1 -or (Get-Process -Id $listener[0].OwningProcess).ProcessName -ne 'omnara-api') {
    throw "omnara-api must be listening on 127.0.0.1:$BackendPort. Set -BackendPort to the -ApiPort used by Install-Services.ps1."
}
try {
    $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$BackendPort/" -Headers @{ Host = "localhost:$Port" } -TimeoutSec 3
    $backendReady = $response.StatusCode -eq 200 -and $response.Headers['Content-Type'] -like 'text/html*'
} catch { $backendReady = $false }
if (-not $backendReady) {
    throw "omnara-api must serve its embedded dashboard on 127.0.0.1:$BackendPort. Check that -BackendPort matches -ApiPort and the API is ready."
}
$siteName = 'Omnara'
$farmName = 'OmnaraControlPlane'
$binding = "127.0.0.1:${Port}:"
. (Join-Path $PSScriptRoot 'Common.ps1')
$SiteDirectory = Resolve-LocalPath $SiteDirectory
if ((Test-Path -LiteralPath $SiteDirectory) -and
    @(Get-ChildItem -LiteralPath $SiteDirectory -Force).Count -gt 0) {
    throw 'SiteDirectory must be empty; existing site content will not be overwritten.'
}
$template = Join-Path $PSScriptRoot 'web.config'
if (-not (Test-Path -LiteralPath $template -PathType Leaf)) { throw 'Place web.config beside this script.' }
$administrationAssembly = Join-Path $env:windir 'System32\inetsrv\Microsoft.Web.Administration.dll'
if (-not (Test-Path -LiteralPath $administrationAssembly)) { throw 'Install IIS before running this script.' }
Add-Type -Path $administrationAssembly

$manager = New-Object Microsoft.Web.Administration.ServerManager
try {
    $config = $manager.GetApplicationHostConfiguration()
    $modules = @($config.GetSection('system.webServer/globalModules').GetCollection() |
        ForEach-Object { $_['name'] })
    foreach ($module in @('WebSocketModule', 'RewriteModule', 'ApplicationRequestRouting')) {
        if ($module -notin $modules) { throw "Required IIS module is missing: $module" }
    }
    $farms = $config.GetSection('webFarms').GetCollection()
    if (@($farms | Where-Object { $_['name'] -eq $farmName }).Count -gt 0) {
        throw "ARR farm $farmName already exists; this script only performs initial configuration."
    }
    if ($manager.Sites[$siteName] -or $manager.ApplicationPools[$siteName]) {
        throw 'The Omnara site or application pool already exists; this script only performs initial configuration.'
    }
    foreach ($otherSite in $manager.Sites) {
        foreach ($application in $otherSite.Applications) {
            if ($application.ApplicationPoolName -eq $siteName) {
                throw 'The Omnara application pool must not be shared with another application.'
            }
        }
        foreach ($otherBinding in $otherSite.Bindings) {
            if ($otherBinding.Protocol -eq 'http' -and $otherBinding.EndPoint.Port -eq $Port -and
                $otherBinding.EndPoint.Address.ToString() -in @('0.0.0.0', '127.0.0.1')) {
                throw "Another IIS site already has an HTTP binding on port $Port."
            }
        }
    }
    # ARR's direct proxy section is server-wide. A dedicated farm scopes these
    # protocol settings without changing the proxy policy for existing sites.
    # https://learn.microsoft.com/iis/extensions/configuring-application-request-routing-arr/define-and-configure-an-application-request-routing-server-farm
    $farm = $farms.CreateElement('webFarm')
    $farm['name'] = $farmName
    $backend = $farm.GetCollection().CreateElement('server')
    $backend['address'] = '127.0.0.1'
    $backend.GetChildElement('applicationRequestRouting')['httpPort'] = [uint32]$BackendPort
    [void] $farm.GetCollection().Add($backend)
    $protocol = $farm.GetChildElement('applicationRequestRouting').GetChildElement('protocol')
    $protocol['preserveHostHeader'] = $true
    $protocol['reverseRewriteHostInResponseHeaders'] = $false
    $protocol['timeout'] = [TimeSpan]::FromHours(1)
    $protocol['minResponseBuffer'] = [uint32]0
    $protocol['responseBufferLimit'] = [uint32]0
    $protocol['bufferChunkedResponses'] = $false
    $protocol['includePortInXForwardedFor'] = $false
    $protocol.GetChildElement('cache')['enabled'] = $false
    [void] $farms.Add($farm)

    $pool = $manager.ApplicationPools.Add($siteName)
    $pool.ManagedRuntimeVersion = ''
    $pool.ProcessModel.IdentityType = [Microsoft.Web.Administration.ProcessModelIdentityType]::ApplicationPoolIdentity
    # A scheduled pool recycle would disconnect long-lived streams.
    $pool.Recycling.PeriodicRestart.Time = [TimeSpan]::Zero
    $site = $manager.Sites.Add($siteName, 'http', $binding, $SiteDirectory)
    $site.Applications['/'].ApplicationPoolName = $siteName
    $config.GetSection('system.webServer/webSocket', $siteName)['enabled'] = $true
    $anonymous = $config.GetSection('system.webServer/security/authentication/anonymousAuthentication', $siteName)
    $anonymous['enabled'] = $true
    $anonymous['userName'] = '' # Use this site's application pool identity.

    [void] (New-Item -ItemType Directory -Path $SiteDirectory -Force)
    $manager.CommitChanges()

    Set-PrivatePermissions $SiteDirectory @('IIS AppPool\Omnara')
    Copy-Item -LiteralPath $template -Destination (Join-Path $SiteDirectory 'web.config')
} finally {
    $manager.Dispose()
}

$deadline = [DateTime]::UtcNow.AddSeconds(30)
$ready = $false
do {
    try {
        $runtime = New-Object Microsoft.Web.Administration.ServerManager
        try {
            $pool = $runtime.ApplicationPools[$siteName]
            $site = $runtime.Sites[$siteName]
            if ($pool.State -eq [Microsoft.Web.Administration.ObjectState]::Stopped) { [void] $pool.Start() }
            if ($site.State -eq [Microsoft.Web.Administration.ObjectState]::Stopped) { [void] $site.Start() }
        } finally {
            $runtime.Dispose()
        }
        $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$Port/" -Headers @{ Host = "localhost:$Port" } -TimeoutSec 3
        $ready = $response.StatusCode -eq 200 -and $response.Headers['Content-Type'] -like 'text/html*'
    } catch { $ready = $false }
    if (-not $ready) { Start-Sleep -Seconds 1 }
} while (-not $ready -and [DateTime]::UtcNow -lt $deadline)
if (-not $ready) {
    throw "IIS did not return the embedded Omnara dashboard from backend port $BackendPort. Check that -BackendPort matches -ApiPort, then inspect the Omnara services and IIS logs."
}
Write-Host "Omnara is available through IIS at http://localhost:$Port using the $farmName farm."
Write-Host 'The IIS binding is loopback only; use an SSH port forward to access it from another machine.'
