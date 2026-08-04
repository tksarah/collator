$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$work = Join-Path $root "work\e2e"
$secrets = Join-Path $work "secrets"
$compose = @("compose", "-f", "compose.yaml", "-f", "tests/compose.e2e.yaml", "--env-file", "work/e2e/.env")

function Write-Secret([string]$Name, [string]$Value) {
  Set-Content -LiteralPath (Join-Path $secrets $Name) -Value $Value -NoNewline -Encoding ascii
}
function Post-Json([string]$Path, [hashtable]$Body, [string[]]$Extra = @()) {
  $bodyFile = Join-Path $work "request.json"
  $Body | ConvertTo-Json -Compress | Set-Content -LiteralPath $bodyFile -NoNewline -Encoding ascii
  $arguments = @("-kfsS", "-H", "Content-Type: application/json") + $Extra + @("--data-binary", "@$bodyFile", "https://localhost:18443$Path")
  $result = & curl.exe @arguments
  if ($LASTEXITCODE -ne 0) { throw "HTTP request failed: $Path" }
  return ($result | Out-String)
}

New-Item -ItemType Directory -Path $secrets, (Join-Path $work "backups") -Force | Out-Null
$random = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($random)
$encryptionKey = [Convert]::ToBase64String($random)
$postgresPassword = "guardian_e2e_2026"
$bootstrapToken = "bootstrap-e2e-token-2026"
Write-Secret "postgres_password" $postgresPassword
Write-Secret "database_url" "postgres://guardian:${postgresPassword}@postgres:5432/guardian?sslmode=disable"
Write-Secret "gemini_api_key" "not-used-in-e2e"
Write-Secret "smtp_password" "not-used-in-e2e"
Write-Secret "bootstrap_token" $bootstrapToken
Write-Secret "encryption_key" $encryptionKey

$envText = @"
DOMAIN=localhost
ACME_EMAIL=operator@example.com
COOKIE_SECURE=true
NODE_NAME=tk_sdn_collator
SYSTEMD_UNIT=astar.service
TZ=Asia/Tokyo
INSTALLED_AT=2026-08-04T00:00:00Z
OBSERVE_ONLY_DAYS=14
OBSERVE_GID=2001
CONTROL_GID=2002
SECRETS_GID=2003
HOST_UID=1000
HOST_GID=1000
SMTP_HOST=
SMTP_PORT=587
SMTP_USER=
SMTP_FROM=
SMTP_TO=
SMTP_UNIX_SOCKET=
SMTP_TLS_MODE=required
GEMINI_MODEL=gemini-3.6-flash
EXTERNAL_RPC_URLS=http://127.0.0.1:9,http://127.0.0.1:9
SECRETS_DIR=./work/e2e/secrets
BACKUP_DIR=./work/e2e/backups
ENV_FILE=work/e2e/.env
"@
Set-Content -LiteralPath (Join-Path $work ".env") -Value $envText -Encoding ascii

Push-Location $root
$passed = $false
try {
  & docker @compose up -d --build --wait --wait-timeout 180
  if ($LASTEXITCODE -ne 0) { throw "Compose did not become healthy" }
  $health = & curl.exe -kfsS https://localhost:18443/healthz
  if ($LASTEXITCODE -ne 0 -or $health -notmatch 'ok') { throw "HTTPS/API health check failed" }

  $setupRaw = Post-Json "/api/v1/auth/bootstrap/start" @{ token=$bootstrapToken; username="e2eadmin"; password="E2e!GuardianPassword2026" }
  $setup = $setupRaw | ConvertFrom-Json
  $totp = (& python -c "import base64,hmac,hashlib,struct,time,sys; k=base64.b32decode(sys.argv[1]); c=int(time.time())//30; h=hmac.new(k,struct.pack('>Q',c),hashlib.sha1).digest(); o=h[-1]&15; print(str((struct.unpack('>I',h[o:o+4])[0]&0x7fffffff)%1000000).zfill(6))" $setup.secret).Trim()
  [void](Post-Json "/api/v1/auth/bootstrap/confirm" @{ token=$bootstrapToken; username="e2eadmin"; totp_code=$totp })

  $headers = Join-Path $work "headers.txt"
  $cookies = Join-Path $work "cookies.txt"
  $login = Post-Json "/api/v1/auth/login" @{ username="e2eadmin"; password="E2e!GuardianPassword2026"; totp_code=$totp } @("-D", $headers, "-c", $cookies)
  if ($login -notmatch 'e2eadmin') { throw "Login failed" }
  $overview = & curl.exe -kfsS -b $cookies https://localhost:18443/api/v1/overview
  if ($LASTEXITCODE -ne 0 -or $overview -notmatch 'tk_sdn_collator') { throw "Authenticated overview failed" }
  $csrf = ((Get-Content -LiteralPath $headers | Where-Object { $_ -match '^X-CSRF-Token:' }) -replace '^X-CSRF-Token:\s*','').Trim()
  [void](Post-Json "/api/v1/auth/logout" @{} @("-b", $cookies, "-H", "X-CSRF-Token: $csrf"))
  Write-Host "PASS Compose HTTPS + PostgreSQL + bootstrap TOTP + login/CSRF/logout E2E"
  $passed = $true
} finally {
  if (-not $passed) { & docker @compose logs --tail=100 }
  & docker @compose down -v --remove-orphans
  Pop-Location
  $resolvedRoot = [IO.Path]::GetFullPath($root).TrimEnd('\') + '\'
  $resolvedWork = [IO.Path]::GetFullPath($work)
  if ($resolvedWork.StartsWith($resolvedRoot, [StringComparison]::OrdinalIgnoreCase) -and (Split-Path $resolvedWork -Leaf) -eq "e2e") {
    Remove-Item -LiteralPath $resolvedWork -Recurse -Force -ErrorAction SilentlyContinue
  }
}
