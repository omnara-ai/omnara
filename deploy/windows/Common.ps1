# Shared installation helpers. Dot-source from the Windows setup scripts.
$WinSWHash = '4ebd9dc3e817f80b2bd8c18f5bfe0d550241c1d08b57bd9694777a6e56a7b24f'

function Resolve-LocalPath([string] $Path) {
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    if ($full -notmatch '^[A-Za-z]:\\' -or $full.Contains('%')) {
        throw 'Installation paths must be local absolute paths without percent signs.'
    }
    # Refuse junctions, including in ancestors, before replacing any ACLs.
    $cursor = $full
    while ($cursor) {
        if ((Test-Path -LiteralPath $cursor) -and
            ((Get-Item -LiteralPath $cursor -Force).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
            throw "Installation path contains a reparse point: $cursor"
        }
        $cursor = [IO.Path]::GetDirectoryName($cursor)
    }
    return $full
}

function Set-PrivatePermissions(
    [string] $Path,
    [string[]] $Readers = @(),
    [Security.AccessControl.FileSystemRights] $Rights = 'ReadAndExecute'
) {
    $item = Get-Item -LiteralPath $Path -Force
    $inherit = [Security.AccessControl.InheritanceFlags]::None
    if ($item.PSIsContainer) {
        $acl = New-Object Security.AccessControl.DirectorySecurity
        $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    } else {
        $acl = New-Object Security.AccessControl.FileSecurity
    }
    $acl.SetAccessRuleProtection($true, $false)
    $administrators = New-Object Security.Principal.SecurityIdentifier('S-1-5-32-544')
    $acl.SetOwner($administrators)
    foreach ($sid in @('S-1-5-32-544', 'S-1-5-18')) {
        $identity = New-Object Security.Principal.SecurityIdentifier($sid)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $identity, 'FullControl', $inherit, 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    foreach ($reader in $Readers) {
        $identity = New-Object Security.Principal.NTAccount($reader)
        $rule = New-Object Security.AccessControl.FileSystemAccessRule(
            $identity, $Rights, $inherit, 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Protect-ProgramDirectory([string] $Path, [string[]] $Accounts) {
    Set-PrivatePermissions $Path $Accounts
    if (@(Get-ChildItem -LiteralPath $Path -Force).Count -gt 0) {
        # An old owner could otherwise change the DACL and regain write access.
        & icacls.exe (Join-Path $Path '*') /setowner '*S-1-5-32-544' /T /Q | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not set program-file ownership in $Path" }
        # Reset children, not the root: inherit only the ACL just installed above.
        & icacls.exe (Join-Path $Path '*') /reset /T /Q | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "Could not protect program files in $Path" }
    }
}

function Add-XmlElement($Parent, [string] $Name, [string] $Value) {
    $node = $Parent.OwnerDocument.CreateElement($Name)
    $node.InnerText = $Value
    [void] $Parent.AppendChild($node)
    return $node
}

function Add-ServiceDefaults($Root, [string] $ID, [string] $LogDirectory) {
    foreach ($entry in ([ordered]@{
        startmode = 'Automatic'; delayedAutoStart = 'true'; stoptimeout = '45 sec'
        stopparentprocessfirst = 'true'; logpath = $LogDirectory
    }).GetEnumerator()) { [void] (Add-XmlElement $Root $entry.Key $entry.Value) }
    $account = Add-XmlElement $Root 'serviceaccount' ''
    [void] (Add-XmlElement $account 'domain' 'NT SERVICE')
    [void] (Add-XmlElement $account 'user' $ID)
    $failure = Add-XmlElement $Root 'onfailure' ''
    $failure.SetAttribute('action', 'restart')
    $failure.SetAttribute('delay', '10 sec')
    $log = Add-XmlElement $Root 'log' ''
    $log.SetAttribute('mode', 'roll-by-size')
    [void] (Add-XmlElement $log 'sizeThreshold' '10240')
    [void] (Add-XmlElement $log 'keepFiles' '8')
}
