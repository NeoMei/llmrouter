$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Fail([string]$Message) {
  throw "LLMROUTER installer: $Message"
}

function Get-ServerOrigin {
  if ([string]::IsNullOrWhiteSpace($env:LLMROUTER_SERVER_URL)) { Fail 'LLMROUTER_SERVER_URL is required' }
  $serverUri = $null
  if (-not [System.Uri]::TryCreate($env:LLMROUTER_SERVER_URL, [System.UriKind]::Absolute, [ref]$serverUri)) { Fail 'server URL is invalid' }
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
  $acl.SetOwner($currentSid)
  foreach ($rule in @($acl.Access)) { [void]$acl.RemoveAccessRuleAll($rule) }
  $inheritance = if ((Get-Item -LiteralPath $Path -Force).PSIsContainer) { [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit' } else { [Security.AccessControl.InheritanceFlags]::None }
  $rule = [Security.AccessControl.FileSystemAccessRule]::new($currentSid, [Security.AccessControl.FileSystemRights]::FullControl, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
  $acl.AddAccessRule($rule)
  Set-Acl -LiteralPath $Path -AclObject $acl

  $verified = Get-Acl -LiteralPath $Path
  $ownerSid = $verified.GetOwner([Security.Principal.SecurityIdentifier])
  $allowed = @($verified.Access | Where-Object {
    $_.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
    $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -eq $currentSid.Value -and
    ($_.FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -eq [Security.AccessControl.FileSystemRights]::FullControl
  })
  if ($ownerSid.Value -ne $currentSid.Value -or $allowed.Count -ne 1 -or @($verified.Access).Count -ne 1) { Fail "current-user owner and ACL could not be enforced: $Path" }
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

function Download-InstallAsset([string]$Origin, [string]$Path, [string]$Destination) {
  try {
    $response = Invoke-WebRequest -Uri "$Origin$Path" -OutFile $Destination -PassThru -MaximumRedirection 0 -ErrorAction Stop
  } catch {
    Fail "download failed or redirect was rejected: $Path"
  }
  if ($null -eq $response -or $response.StatusCode -lt 200 -or $response.StatusCode -ge 300) { Fail "download returned an invalid status: $Path" }
}

function Move-PrivateItem([string]$Source, [string]$Destination) {
  Move-Item -LiteralPath $Source -Destination $Destination -Force
}

function Assert-SafeExistingHelper([string]$Path) {
  if (Test-Path -LiteralPath $Path) {
    $existing = Get-Item -LiteralPath $Path -Force
    if ($existing.PSIsContainer -or ($existing.Attributes -band [IO.FileAttributes]::ReparsePoint)) { Fail "unsafe helper: $Path" }
    Set-PrivateAcl $Path
  }
}

function Assert-HelperDigest([string]$Path, [string]$Expected) {
  if ((Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $Expected) { Fail "helper checksum mismatch: $Path" }
}

function Install-PrivateHelperPair([string]$TemporaryDirectory, [string]$OnboardPath, [string]$BridgePath, [string]$OnboardTarget, [string]$BridgeTarget, [string]$OnboardChecksum, [string]$BridgeChecksum) {
  $backupDirectory = Join-Path $TemporaryDirectory 'backups'
  $onboardOriginal = Test-Path -LiteralPath $OnboardTarget
  $bridgeOriginal = Test-Path -LiteralPath $BridgeTarget
  $onboardReplaced = $false
  $bridgeReplaced = $false
  $committed = $false
  $rollbackFailed = $false
  $operationError = $null
  try {
    Assert-SafeExistingHelper $OnboardTarget
    Assert-SafeExistingHelper $BridgeTarget
    Set-PrivateAcl $OnboardPath
    Set-PrivateAcl $BridgePath
    New-Item -ItemType Directory -LiteralPath $backupDirectory | Out-Null
    Set-PrivateAcl $backupDirectory
    if ($onboardOriginal) { Move-PrivateItem $OnboardTarget (Join-Path $backupDirectory 'llmrouter-onboard.exe') }
    if ($bridgeOriginal) { Move-PrivateItem $BridgeTarget (Join-Path $backupDirectory 'llmrouter-mcp-bridge.exe') }
    Move-PrivateItem $OnboardPath $OnboardTarget
    $onboardReplaced = $true
    Set-PrivateAcl $OnboardTarget
    Move-PrivateItem $BridgePath $BridgeTarget
    $bridgeReplaced = $true
    Set-PrivateAcl $BridgeTarget
    Assert-HelperDigest $OnboardTarget $OnboardChecksum
    Assert-HelperDigest $BridgeTarget $BridgeChecksum
    $committed = $true
  } catch {
    $operationError = $_
  } finally {
    if (-not $committed) {
      if ($onboardOriginal -and (Test-Path -LiteralPath (Join-Path $backupDirectory 'llmrouter-onboard.exe'))) {
        try { Move-PrivateItem (Join-Path $backupDirectory 'llmrouter-onboard.exe') $OnboardTarget } catch { $rollbackFailed = $true }
      } elseif ($onboardReplaced) { Remove-Item -LiteralPath $OnboardTarget -Force -ErrorAction SilentlyContinue }
      if ($bridgeOriginal -and (Test-Path -LiteralPath (Join-Path $backupDirectory 'llmrouter-mcp-bridge.exe'))) {
        try { Move-PrivateItem (Join-Path $backupDirectory 'llmrouter-mcp-bridge.exe') $BridgeTarget } catch { $rollbackFailed = $true }
      } elseif ($bridgeReplaced) { Remove-Item -LiteralPath $BridgeTarget -Force -ErrorAction SilentlyContinue }
    }
    if (-not $rollbackFailed -and (Test-Path -LiteralPath $backupDirectory)) { Remove-Item -LiteralPath $backupDirectory -Recurse -Force }
  }
  if ($rollbackFailed) { Fail "rollback could not restore every helper; recover from $backupDirectory" }
  if ($null -ne $operationError) { throw $operationError }
  if (-not $committed) { Fail 'could not transactionally install both helpers' }
}

$origin = Get-ServerOrigin
$architecture = switch -Regex ($env:PROCESSOR_ARCHITECTURE) {
  '^(AMD64|x86_64)$' { 'amd64'; break }
  '^(ARM64|aarch64)$' { 'arm64'; break }
  default { Fail 'unsupported processor architecture' }
}
$home = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
if ([string]::IsNullOrWhiteSpace($home) -or -not [IO.Path]::IsPathRooted($home)) { Fail 'user profile is unavailable' }
$llmapiDirectory = Join-Path $home '.llmrouter'
$binDirectory = Join-Path $llmapiDirectory 'bin'
Ensure-PrivateDirectory $llmapiDirectory
Ensure-PrivateDirectory $binDirectory

$temporaryDirectory = $null
$retainTemporaryDirectory = $false
try {
  $temporaryFile = New-TemporaryFile
  Remove-Item -LiteralPath $temporaryFile -Force
  $temporaryDirectory = Join-Path $binDirectory ('.install-' + [IO.Path]::GetFileName($temporaryFile))
  New-Item -ItemType Directory -LiteralPath $temporaryDirectory | Out-Null
  Set-PrivateAcl $temporaryDirectory

  $onboardArtifact = "llmrouter-onboard-windows-$architecture.exe"
  $bridgeArtifact = "llmrouter-mcp-bridge-windows-$architecture.exe"
  $checksumsPath = Join-Path $temporaryDirectory 'checksums.txt'
  $onboardPath = Join-Path $temporaryDirectory $onboardArtifact
  $bridgePath = Join-Path $temporaryDirectory $bridgeArtifact
  Download-InstallAsset $origin '/install/checksums.txt' $checksumsPath
  Download-InstallAsset $origin "/install/bin/$onboardArtifact" $onboardPath
  Download-InstallAsset $origin "/install/bin/$bridgeArtifact" $bridgePath

  $onboardChecksum = Get-ExpectedChecksum $checksumsPath $onboardArtifact
  $bridgeChecksum = Get-ExpectedChecksum $checksumsPath $bridgeArtifact
  Assert-HelperDigest $onboardPath $onboardChecksum
  Assert-HelperDigest $bridgePath $bridgeChecksum

  $onboardTarget = Join-Path $binDirectory 'llmrouter-onboard.exe'
  $bridgeTarget = Join-Path $binDirectory 'llmrouter-mcp-bridge.exe'
  try {
    Install-PrivateHelperPair $temporaryDirectory $onboardPath $bridgePath $onboardTarget $bridgeTarget $onboardChecksum $bridgeChecksum
  } catch {
    if ($_.Exception.Message -like 'LLMROUTER installer: rollback could not restore every helper*') { $retainTemporaryDirectory = $true }
    throw
  }
  Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force
  $temporaryDirectory = $null
  & $onboardTarget install
  if ($LASTEXITCODE -ne 0) { Fail 'onboarding helper failed' }
} finally {
  if (-not $retainTemporaryDirectory -and $null -ne $temporaryDirectory -and (Test-Path -LiteralPath $temporaryDirectory)) { Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force }
}
