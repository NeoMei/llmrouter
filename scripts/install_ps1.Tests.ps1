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
  It 'reads the exact two-space checksum entry for each helper' {
    $functionSource = $script.Substring(0, $script.IndexOf('$origin = Get-ServerOrigin'))
    Invoke-Expression $functionSource
    $path = Join-Path $TestDrive 'checksums.txt'
    $digest = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
    Set-Content -LiteralPath $path -Value "$digest  llmapi-mcp-bridge-windows-amd64.exe"
    Get-ExpectedChecksum $path 'llmapi-mcp-bridge-windows-amd64.exe' | Should -Be $digest
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
