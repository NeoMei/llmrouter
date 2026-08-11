$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Fail([string]$Message) {
  throw "LLMAPI installer: $Message"
}

function Get-ServerOrigin {
  if ([string]::IsNullOrWhiteSpace($env:LLMAPI_SERVER_URL)) { Fail 'LLMAPI_SERVER_URL is required' }
  $serverUri = $null
  if (-not [System.Uri]::TryCreate($env:LLMAPI_SERVER_URL, [System.UriKind]::Absolute, [ref]$serverUri)) { Fail 'server URL is invalid' }
  if ($serverUri.UserInfo -or $serverUri.Query -or $serverUri.Fragment -or ($serverUri.AbsolutePath -ne '/' -and $serverUri.AbsolutePath -ne '')) { Fail 'server URL must be an origin' }
  if ($serverUri.Scheme -ne 'https') {
    if ($serverUri.Scheme -ne 'http' -or $serverUri.Host -notin @('localhost', '127.0.0.1', '::1')) { Fail 'server URL must use HTTPS or loopback HTTP' }
  }
  return $serverUri.GetLeftPart([System.UriPartial]::Authority)
}

function Assert-NotReparsePoint([string]$Path) {
  if ((Get-Item -LiteralPath $Path -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { Fail "unsafe reparse point: $Path" }
}

function Set-PrivateAcl([string]$Path) {
  Assert-NotReparsePoint $Path
  $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
  if ($null -eq $currentSid) { Fail 'current user SID is unavailable' }
  $acl = Get-Acl -LiteralPath $Path
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRuleAll($rule) }
  $inheritance = if ((Get-Item -LiteralPath $Path -Force).PSIsContainer) { [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' } else { [Security.AccessControl.InheritanceFlags]::None }
  $rule = [Security.AccessControl.FileSystemAccessRule]::new($currentSid, [Security.AccessControl.FileSystemRights]::FullControl, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
  $acl.AddAccessRule($rule)
  Set-Acl -LiteralPath $Path -AclObject $acl

  $verified = Get-Acl -LiteralPath $Path
  $allowed = @($verified.Access | Where-Object {
    $_.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
    $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -eq $currentSid.Value -and
    ($_.FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -eq [Security.AccessControl.FileSystemRights]::FullControl
  })
  if ($allowed.Count -ne 1 -or @($verified.Access).Count -ne 1) { Fail "current-user ACL could not be enforced: $Path" }
}

function Ensure-PrivateDirectory([string]$Path) {
  if (Test-Path -LiteralPath $Path) {
    if (-not (Get-Item -LiteralPath $Path -Force).PSIsContainer) { Fail "unsafe directory: $Path" }
  } else {
    New-Item -ItemType Directory -LiteralPath $Path | Out-Null
  }
  Set-PrivateAcl $Path
}

function Get-ExpectedChecksum([string]$ChecksumsPath, [string]$Artifact) {
  $seenLines = @{}
  $seenNames = @{}
  $selectedDigests = @()
  foreach ($line in Get-Content -LiteralPath $ChecksumsPath -Encoding UTF8) {
    if ($line -notmatch '^(?<digest>[0-9A-Fa-f]{64})  (?<name>[A-Za-z0-9._-]+)$') { Fail 'checksum file has invalid grammar' }
    $name = $Matches.name
    if ($name.Contains('..') -or $seenLines.ContainsKey($line) -or $seenNames.ContainsKey($name)) { Fail 'checksum file contains duplicate or unsafe artifacts' }
    $seenLines[$line] = $true
    $seenNames[$name] = $true
    if ($name -eq $Artifact) { $selectedDigests += $Matches.digest.ToLowerInvariant() }
  }
  if ($selectedDigests.Count -ne 1) { Fail "checksum is missing for $Artifact" }
  return $selectedDigests[0]
}

function Download-InstallAsset([string]$Origin, [string]$Name, [string]$Destination) {
  Invoke-WebRequest -Uri "$Origin/install/$Name" -OutFile $Destination -MaximumRedirection 0
}

function Install-PrivateHelper([string]$Source, [string]$Destination) {
  if (Test-Path -LiteralPath $Destination) {
    $existing = Get-Item -LiteralPath $Destination -Force
    if ($existing.PSIsContainer -or ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) { Fail "unsafe helper: $Destination" }
  }
  Set-PrivateAcl $Source
  Move-Item -LiteralPath $Source -Destination $Destination -Force
  Set-PrivateAcl $Destination
}

$origin = Get-ServerOrigin
$architecture = switch -Regex ($env:PROCESSOR_ARCHITECTURE) {
  '^(AMD64|x86_64)$' { 'amd64'; break }
  '^(ARM64|aarch64)$' { 'arm64'; break }
  default { Fail 'unsupported processor architecture' }
}
$home = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
if ([string]::IsNullOrWhiteSpace($home) -or -not [IO.Path]::IsPathRooted($home)) { Fail 'user profile is unavailable' }
$llmapiDirectory = Join-Path $home '.llmapi'
$binDirectory = Join-Path $llmapiDirectory 'bin'
Ensure-PrivateDirectory $llmapiDirectory
Ensure-PrivateDirectory $binDirectory

$temporaryDirectory = $null
try {
  $temporaryFile = New-TemporaryFile
  Remove-Item -LiteralPath $temporaryFile -Force
  $temporaryDirectory = Join-Path $binDirectory ('.install-' + [IO.Path]::GetFileName($temporaryFile))
  New-Item -ItemType Directory -LiteralPath $temporaryDirectory | Out-Null
  Set-PrivateAcl $temporaryDirectory

  $onboardArtifact = "llmapi-onboard-windows-$architecture.exe"
  $bridgeArtifact = "llmapi-mcp-bridge-windows-$architecture.exe"
  $checksumsPath = Join-Path $temporaryDirectory 'checksums.txt'
  $onboardPath = Join-Path $temporaryDirectory $onboardArtifact
  $bridgePath = Join-Path $temporaryDirectory $bridgeArtifact
  Download-InstallAsset $origin 'checksums.txt' $checksumsPath
  Download-InstallAsset $origin $onboardArtifact $onboardPath
  Download-InstallAsset $origin $bridgeArtifact $bridgePath

  $onboardChecksum = Get-ExpectedChecksum $checksumsPath $onboardArtifact
  $bridgeChecksum = Get-ExpectedChecksum $checksumsPath $bridgeArtifact
  if ((Get-FileHash -LiteralPath $onboardPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $onboardChecksum) { Fail 'onboarding helper checksum mismatch' }
  if ((Get-FileHash -LiteralPath $bridgePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $bridgeChecksum) { Fail 'MCP helper checksum mismatch' }

  $onboardTarget = Join-Path $binDirectory 'llmapi-onboard.exe'
  $bridgeTarget = Join-Path $binDirectory 'llmapi-mcp-bridge.exe'
  Install-PrivateHelper $onboardPath $onboardTarget
  Install-PrivateHelper $bridgePath $bridgeTarget
  & $onboardTarget install
  if ($LASTEXITCODE -ne 0) { Fail 'onboarding helper failed' }
} finally {
  if ($null -ne $temporaryDirectory -and (Test-Path -LiteralPath $temporaryDirectory)) { Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force }
}
