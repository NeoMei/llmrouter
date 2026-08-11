Describe 'LLMAPI PowerShell bootstrap' {
  BeforeAll { $script = Get-Content "$PSScriptRoot/../web/public/install/install.ps1" -Raw }

  It 'has no token parameter' { $script | Should -Not -Match '--(api-)?token' }
  It 'requires SHA-256 verification' { $script | Should -Match 'Get-FileHash.+SHA256' }
  It 'uses the explicit server environment' { $script | Should -Match 'LLMAPI_SERVER_URL' }
  It 'always cleans temporary files' { $script | Should -Match 'finally' }
  It 'installs helpers privately before onboarding' {
    $script | Should -Match 'llmapi-mcp-bridge'
    $script | Should -Match 'Set-Acl'
    $script | Should -Match 'Move-Item'
  }
  It 'uses bin artifact URLs and rejects redirects before downloading helpers' {
    $script | Should -Match '/install/bin/\$onboardArtifact'
    $script | Should -Match '/install/bin/\$bridgeArtifact'
    $script | Should -Match 'MaximumRedirection 0'
    $script | Should -Match 'StatusCode -lt 200'
  }
  It 'sets and verifies the current user as owner' {
    $script | Should -Match 'SetOwner\(\$currentSid\)'
    $script | Should -Match 'GetOwner\(\[Security.Principal.SecurityIdentifier\]\)'
    $script | Should -Match 'ownerSid.Value -ne \$currentSid.Value'
  }
  It 'removes temporary downloads before launching onboarding' {
    $script.IndexOf('Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force') | Should -BeLessThan $script.IndexOf('& $onboardTarget install')
  }
  It 'reads the exact two-space checksum entry for each helper' {
    $functionSource = $script.Substring(0, $script.IndexOf('$origin = Get-ServerOrigin'))
    Invoke-Expression $functionSource
    $path = Join-Path $TestDrive 'checksums.txt'
    $digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    Set-Content -LiteralPath $path -Value "$digest  llmapi-mcp-bridge-windows-amd64.exe"
    Get-ExpectedChecksum $path 'llmapi-mcp-bridge-windows-amd64.exe' | Should -Be $digest
  }
  It 'sets and verifies owner SID on a real TestDrive path' -Skip:($env:OS -ne 'Windows_NT') {
    $functionSource = $script.Substring(0, $script.IndexOf('$origin = Get-ServerOrigin'))
    Invoke-Expression $functionSource
    $path = Join-Path $TestDrive 'owner-check'
    Set-Content -LiteralPath $path -Value 'private'
    Set-PrivateAcl $path
    $owner = (Get-Acl -LiteralPath $path).GetOwner([Security.Principal.SecurityIdentifier])
    $owner.Value | Should -Be ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
  }
  It 'rolls back both original helpers when the second replacement fails' {
    $functionSource = $script.Substring(0, $script.IndexOf('$origin = Get-ServerOrigin'))
    Invoke-Expression $functionSource
    $bin = Join-Path $TestDrive 'bin'
    $temp = Join-Path $bin '.install-test'
    New-Item -ItemType Directory -Path $temp -Force | Out-Null
    $onboardTarget = Join-Path $bin 'llmapi-onboard.exe'
    $bridgeTarget = Join-Path $bin 'llmapi-mcp-bridge.exe'
    Set-Content -LiteralPath $onboardTarget -Value 'old-onboard' -NoNewline
    Set-Content -LiteralPath $bridgeTarget -Value 'old-bridge' -NoNewline
    $onboardSource = Join-Path $temp 'new-onboard.exe'
    $bridgeSource = Join-Path $temp 'new-bridge.exe'
    Set-Content -LiteralPath $onboardSource -Value 'new-onboard' -NoNewline
    Set-Content -LiteralPath $bridgeSource -Value 'new-bridge' -NoNewline
    Mock Set-PrivateAcl {}
    Mock Assert-SafeExistingHelper {}
    Mock Assert-HelperDigest {}
    Mock Move-PrivateItem {
      param($Source, $Destination)
      if ($Source -eq $bridgeSource -and $Destination -eq $bridgeTarget) { throw 'second replacement failed' }
      Microsoft.PowerShell.Management\Move-Item -LiteralPath $Source -Destination $Destination -Force
    }
    { Install-PrivateHelperPair $temp $onboardSource $bridgeSource $onboardTarget $bridgeTarget ('a' * 64) ('b' * 64) } | Should -Throw 'second replacement failed'
    (Get-Content -LiteralPath $onboardTarget -Raw) | Should -Be 'old-onboard'
    (Get-Content -LiteralPath $bridgeTarget -Raw) | Should -Be 'old-bridge'
  }
  It 'parses as valid PowerShell' {
    $tokens = $null
    $errors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile(
      "$PSScriptRoot/../web/public/install/install.ps1", [ref]$tokens, [ref]$errors
    )
    $errors | Should -BeNullOrEmpty
  }
}
