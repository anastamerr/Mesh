#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^https://|^http://(localhost|127\.0\.0\.1)(:|$)')]
    [string] $Server,

    [Parameter(Mandatory = $true)]
    [string] $StorageRoot,

    [string] $Name = $env:COMPUTERNAME,
    [string] $StateDirectory = (Join-Path $env:LOCALAPPDATA 'Mesh\agent'),
    [switch] $EnableCompute,
    [switch] $DirectLAN,
    [switch] $InstallService
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if (-not [System.IO.Path]::IsPathRooted($StorageRoot) -or
    -not [System.IO.Path]::IsPathRooted($StateDirectory)) {
    throw 'StorageRoot and StateDirectory must be absolute paths.'
}

$agent = Join-Path $PSScriptRoot 'mesh-agent.exe'
$agentDigestFile = "$agent.sha256"
$bundle = Join-Path $PSScriptRoot 'mesh-wsl-rootfs-amd64.tar'
if (-not (Test-Path -LiteralPath $agent -PathType Leaf)) {
    throw 'mesh-agent.exe is missing from this setup folder.'
}
if (-not (Test-Path -LiteralPath $agentDigestFile -PathType Leaf)) {
    throw 'mesh-agent.exe.sha256 is missing from this setup folder.'
}
$expectedAgentDigest = ((Get-Content -LiteralPath $agentDigestFile -Raw).Trim() -split '\s+')[0]
if ($expectedAgentDigest -notmatch '^[a-fA-F0-9]{64}$') {
    throw 'mesh-agent.exe.sha256 does not contain a valid SHA-256 digest.'
}
$actualAgentDigest = (Get-FileHash -LiteralPath $agent -Algorithm SHA256).Hash
if ($actualAgentDigest -ne $expectedAgentDigest) {
    throw 'mesh-agent.exe does not match its packaged SHA-256 digest. Do not run this package.'
}

$arguments = @(
    'setup', '--server', $Server, '--name', $Name,
    '--state-dir', $StateDirectory, '--root', $StorageRoot
)
if ($DirectLAN) {
    $arguments += '--direct-lan'
}
if ($EnableCompute) {
    if (-not (Test-Path -LiteralPath $bundle -PathType Leaf) -or
        -not (Test-Path -LiteralPath "$bundle.sha256" -PathType Leaf)) {
        throw 'The verified compute bundle and its .sha256 file must be beside this script.'
    }
    $arguments += @('--compute-bundle', $bundle)
}

if ($InstallService) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw 'InstallService requires an elevated PowerShell window.'
    }
    $account = "$env:USERDOMAIN\$env:USERNAME"
    $credential = Get-Credential -UserName $account -Message 'Enter the current Windows account password for automatic Mesh startup.'
    if ($credential.UserName -ne $account) {
        throw "The service account must remain $account so it can decrypt the paired identity."
    }
    $arguments += @('--install-service', '--account', $account, '--password-stdin')
    $credential.GetNetworkCredential().Password | & $agent @arguments
} else {
    & $agent @arguments
}

if ($LASTEXITCODE -ne 0) {
    throw "Mesh setup stopped with exit code $LASTEXITCODE. Correct the reported issue and run this same command again."
}
