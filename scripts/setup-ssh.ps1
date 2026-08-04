param(
  [string]$HostName = "192.168.2.194",
  [string]$User = "tk",
  [string]$Alias = "shiden-collator",
  [string]$ExpectedFingerprint = "SHA256:fkbdkdvFESNUXqSzus7FFxWSt/jEs+Itx6YkC9ZMN4U"
)
$ErrorActionPreference = "Stop"
if (Test-Path -LiteralPath "Variable:PSNativeCommandUseErrorActionPreference") {
  $PSNativeCommandUseErrorActionPreference = $false
}
$sshDir = Join-Path $env:USERPROFILE ".ssh"
$keyPath = Join-Path $sshDir "shiden_collator_deploy_ed25519"
$configPath = Join-Path $sshDir "config"
$knownHosts = Join-Path $sshDir "known_hosts"
New-Item -ItemType Directory -Path $sshDir -Force | Out-Null

Write-Host "Reading the SSH Ed25519 host key and verifying its fingerprint..."
$scanOutput = [IO.Path]::GetTempFileName()
$scanError = [IO.Path]::GetTempFileName()
try {
  $sshKeyscan = (Get-Command ssh-keyscan.exe -CommandType Application -ErrorAction Stop).Source
  $scanProcess = Start-Process -FilePath $sshKeyscan `
    -ArgumentList @("-T", "5", "-t", "ed25519", $HostName) `
    -NoNewWindow -Wait -PassThru `
    -RedirectStandardOutput $scanOutput `
    -RedirectStandardError $scanError
  $scan = @(Get-Content -LiteralPath $scanOutput -ErrorAction Stop | Where-Object { $_ -match '^\S+\s+ssh-ed25519\s+' })
  $scanErrorText = (Get-Content -LiteralPath $scanError -Raw -ErrorAction SilentlyContinue).Trim()

  if ($scanProcess.ExitCode -ne 0 -or $scan.Count -eq 0) {
    Write-Host "ssh-keyscan is incompatible with the server KEX; using a fingerprint-checked SSH handshake fallback."
    $fallbackKnownHosts = [IO.Path]::GetTempFileName()
    $fallbackOutput = [IO.Path]::GetTempFileName()
    $fallbackError = [IO.Path]::GetTempFileName()
    try {
      $ssh = (Get-Command ssh.exe -CommandType Application -ErrorAction Stop).Source
      $fallbackProcess = Start-Process -FilePath $ssh `
        -ArgumentList @(
          "-o", "KexAlgorithms=curve25519-sha256",
          "-o", "HostKeyAlgorithms=ssh-ed25519",
          "-o", "BatchMode=yes",
          "-o", "PasswordAuthentication=no",
          "-o", "PubkeyAuthentication=no",
          "-o", "ConnectTimeout=5",
          "-o", "StrictHostKeyChecking=accept-new",
          "-o", "HashKnownHosts=no",
          "-o", "UserKnownHostsFile=$fallbackKnownHosts",
          "$User@$HostName", "exit"
        ) `
        -NoNewWindow -Wait -PassThru `
        -RedirectStandardOutput $fallbackOutput `
        -RedirectStandardError $fallbackError
      $scan = @(Get-Content -LiteralPath $fallbackKnownHosts -ErrorAction Stop | Where-Object { $_ -match '^\S+\s+ssh-ed25519\s+' })
      if ($scan.Count -eq 0) {
        $fallbackErrorText = (Get-Content -LiteralPath $fallbackError -Raw -ErrorAction SilentlyContinue).Trim()
        throw "Host-key handshake failed (exit $($fallbackProcess.ExitCode)); known_hosts was not modified. $fallbackErrorText Original ssh-keyscan error: $scanErrorText"
      }
    } finally {
      Remove-Item -LiteralPath $fallbackKnownHosts, $fallbackOutput, $fallbackError -Force -ErrorAction SilentlyContinue
    }
  }
} finally {
  Remove-Item -LiteralPath $scanOutput, $scanError -Force -ErrorAction SilentlyContinue
}
$temp = [IO.Path]::GetTempFileName()
try {
  Set-Content -LiteralPath $temp -Value $scan -Encoding ascii
  $scanned = (& ssh-keygen -lf $temp | Out-String)
  if ($scanned -notmatch [regex]::Escape($ExpectedFingerprint)) { throw "Scanned host key does not match the verified fingerprint." }
} finally { Remove-Item -LiteralPath $temp -Force -ErrorAction SilentlyContinue }

if (-not (Test-Path -LiteralPath $keyPath)) {
  Write-Host "Create a dedicated key. Enter a strong passphrase when prompted."
  & ssh-keygen -t ed25519 -a 100 -f $keyPath -C "shiden-guardian-deploy"
  if ($LASTEXITCODE -ne 0) { throw "ssh-keygen failed." }
}

$existing = if (Test-Path -LiteralPath $knownHosts) { Get-Content -LiteralPath $knownHosts -Raw } else { "" }
if ($existing -notmatch [regex]::Escape($HostName)) { Add-Content -LiteralPath $knownHosts -Value $scan -Encoding ascii }

$block = @"

Host $Alias
    HostName $HostName
    User $User
    IdentityFile $keyPath
    IdentitiesOnly yes
    StrictHostKeyChecking yes
"@
$config = if (Test-Path -LiteralPath $configPath) { Get-Content -LiteralPath $configPath -Raw } else { "" }
if ($config -notmatch "(?m)^Host\s+$([regex]::Escape($Alias))\s*$") { Add-Content -LiteralPath $configPath -Value $block -Encoding utf8 }

$publicKey = Get-Content -LiteralPath "$keyPath.pub" -Raw
Write-Host "`nVerified fingerprint: $ExpectedFingerprint"
Write-Host "Register this exact line in /home/tk/.ssh/authorized_keys from your existing password session:"
Write-Host ('from="192.168.2.0/24",restrict ' + $publicKey.Trim())
try {
  $agent = Get-Service -Name ssh-agent -ErrorAction Stop
  if ($agent.Status -ne "Running") { Start-Service -Name ssh-agent -ErrorAction Stop }
  & ssh-add $keyPath
  if ($LASTEXITCODE -ne 0) { Write-Warning "ssh-add failed; run it manually before deployment." }
} catch {
  Write-Warning "Windows ssh-agent could not be started automatically. Start it and run: ssh-add $keyPath"
}
Write-Host "`nAfter registering the printed public-key line, test: ssh $Alias hostname"
