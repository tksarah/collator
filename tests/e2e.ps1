$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$work = Join-Path $root "work\e2e"
$secrets = Join-Path $work "secrets"
$compose = @("compose", "-f", "compose.yaml", "-f", "tests/compose.e2e.yaml", "--env-file", "work/e2e/.env")

function Write-Secret([string]$Name, [string]$Value) {
  Set-Content -LiteralPath (Join-Path $secrets $Name) -Value $Value -NoNewline -Encoding ascii
}
function Request-Json([string]$Method, [string]$Path, [hashtable]$Body, [string[]]$Extra = @()) {
  $bodyFile = Join-Path $work "request.json"
  $responseFile = Join-Path $work "response.json"
  $Body | ConvertTo-Json -Compress | Set-Content -LiteralPath $bodyFile -NoNewline -Encoding ascii
  $arguments = @("-ksS", "-o", $responseFile, "-w", "%{http_code}", "--request", $Method, "-H", "Content-Type: application/json") + $Extra + @("--data-binary", "@$bodyFile", "https://localhost:18443$Path")
  $statusText = (& curl.exe @arguments | Out-String).Trim()
  if ($LASTEXITCODE -ne 0) { throw "HTTP request failed: $Path" }
  return [pscustomobject]@{ Status = [int]$statusText; Body = (Get-Content -LiteralPath $responseFile -Raw) }
}
function Post-Json([string]$Path, [hashtable]$Body, [string[]]$Extra = @()) {
  $response = Request-Json "POST" $Path $Body $Extra
  if ($response.Status -lt 200 -or $response.Status -ge 300) { throw "HTTP $($response.Status): $Path $($response.Body)" }
  return $response.Body
}
function Get-Totp([string]$Secret) {
  return (& python -c "import base64,hmac,hashlib,struct,time,sys; k=base64.b32decode(sys.argv[1]); c=int(time.time())//30; h=hmac.new(k,struct.pack('>Q',c),hashlib.sha1).digest(); o=h[-1]&15; print(str((struct.unpack('>I',h[o:o+4])[0]&0x7fffffff)%1000000).zfill(6))" $Secret).Trim()
}
function Get-ActionCount {
  return ((& docker @compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions") | Out-String).Trim()
}
function Get-RewardActionCount {
  return ((& docker @compose exec -T postgres psql -U guardian -d guardian -Atc "SELECT count(*) FROM remediation_actions a JOIN incidents i ON i.id=a.incident_id WHERE i.fingerprint LIKE 'reward-%'") | Out-String).Trim()
}
function Test-AdminCredential([string]$Password) {
  $savedPreference = $ErrorActionPreference
  try {
    $ErrorActionPreference = "SilentlyContinue"
    & docker @compose --profile tools run --rm --no-deps -e "PGPASSWORD=$Password" database-probe -h postgres -U guardian -d guardian -Atc "SELECT 1" *> $null
    return $LASTEXITCODE -eq 0
  } finally {
    $ErrorActionPreference = $savedPreference
  }
}

New-Item -ItemType Directory -Path $secrets, (Join-Path $work "backups") -Force | Out-Null
$random = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($random)
$encryptionKey = [Convert]::ToBase64String($random)
$postgresOldPassword = "guardian_e2e_old_2026"
$postgresPassword = "guardian_e2e_new_2026"
$apiPassword = "guardian_api_e2e_2026"
$authPassword = "guardian_auth_e2e_2026"
$controllerPassword = "guardian_controller_e2e_2026"
$backupPassword = "guardian_backup_e2e_2026"
$bootstrapToken = "bootstrap-e2e-token-2026"
Write-Secret "postgres_password" $postgresOldPassword
Write-Secret "migration_database_url" "postgres://guardian:${postgresOldPassword}@postgres:5432/guardian?sslmode=disable"
Write-Secret "api_database_url" "postgres://guardian_api:${apiPassword}@postgres:5432/guardian?sslmode=disable"
Write-Secret "auth_database_url" "postgres://guardian_auth:${authPassword}@postgres:5432/guardian?sslmode=disable"
Write-Secret "controller_database_url" "postgres://guardian_controller:${controllerPassword}@postgres:5432/guardian?sslmode=disable"
Write-Secret "backup_password" $backupPassword
Write-Secret "gemini_api_key" "not-used-in-e2e"
Write-Secret "smtp_password" "not-used-in-e2e"
Write-Secret "bootstrap_token" $bootstrapToken
Write-Secret "encryption_key" $encryptionKey
$brokerKey = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($brokerKey)
Write-Secret "action_broker_key" ([Convert]::ToBase64String($brokerKey))

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
	& docker @compose up -d postgres --wait --wait-timeout 60
	if ($LASTEXITCODE -ne 0) { throw "PostgreSQL did not become healthy" }
	Write-Secret "postgres_password" $postgresPassword
	& docker @compose --profile tools run --rm --build dbmigrate -mode=dry-run
	if ($LASTEXITCODE -ne 0) { throw "Database role migration dry-run failed" }
	& docker @compose --profile tools run --rm --build dbmigrate -mode=apply
	if ($LASTEXITCODE -ne 0) { throw "Database role migration failed" }
	& docker @compose --profile tools run --rm dbmigrate -mode=verify
	if ($LASTEXITCODE -ne 0) { throw "Database role verification failed" }
	& docker @compose --profile tools run --rm dbmigrate -mode=rotate-admin
	if ($LASTEXITCODE -ne 0) { throw "Database administrator rotation failed" }
	if (Test-AdminCredential $postgresOldPassword) { throw "Old database administrator credential still works" }
	if (-not (Test-AdminCredential $postgresPassword)) { throw "Rotated database administrator credential does not work" }
  & docker @compose up -d --build --wait --wait-timeout 180
  if ($LASTEXITCODE -ne 0) { throw "Compose did not become healthy" }
  $health = & curl.exe -kfsS https://localhost:18443/healthz
  if ($LASTEXITCODE -ne 0 -or $health -notmatch 'ok') { throw "HTTPS/API health check failed" }
	$rewardActionsBefore = Get-RewardActionCount
	& docker @compose exec -T api /bin/sh -c "test -e /run/secrets/api_database_url -a ! -e /run/secrets/auth_database_url -a ! -e /run/secrets/controller_database_url -a ! -e /run/secrets/postgres_password -a ! -e /run/secrets/bootstrap_token -a ! -e /run/secrets/encryption_key -a ! -e /run/secrets/action_broker_key -a ! -e /run/secrets/migration_database_url -a ! -e /run/shiden-guardian/action-broker/controller.sock"
	if ($LASTEXITCODE -ne 0) { throw "API received a privileged secret" }
	& docker @compose exec -T api /bin/sh -c "! getent hosts auth-broker >/dev/null 2>&1"
	if ($LASTEXITCODE -ne 0) { throw "API can resolve auth-broker across an isolated network" }
	$databasePeerProbe = (& docker @compose exec -T postgres /bin/sh -c "wget -S -O - --header='X-Real-IP: 198.51.100.9' http://auth-broker:8081/api/v1/auth/bootstrap/status 2>&1 || true" | Out-String)
	if ($databasePeerProbe -notmatch '403 Forbidden') { throw "Auth broker accepted a non-Caddy network peer: $databasePeerProbe" }
	$removedHandlerProbe = (& docker @compose exec -T caddy /bin/sh -c "wget -S -O - http://api:8080/api/v1/auth/bootstrap/status 2>&1 || true" | Out-String)
	if ($removedHandlerProbe -notmatch '404 Not Found') { throw "API still exposes a removed auth handler: $removedHandlerProbe" }

  $setupRaw = Post-Json "/api/v1/auth/bootstrap/start" @{ token=$bootstrapToken; username="e2eadmin"; password="E2e!GuardianPassword2026" }
  $setup = $setupRaw | ConvertFrom-Json
  $totp = Get-Totp $setup.secret
  [void](Post-Json "/api/v1/auth/bootstrap/confirm" @{ token=$bootstrapToken; username="e2eadmin"; totp_code=$totp })

  $headers = Join-Path $work "headers.txt"
  $cookies = Join-Path $work "cookies.txt"
  $totp = Get-Totp $setup.secret
  $login = Post-Json "/api/v1/auth/login" @{ username="e2eadmin"; password="E2e!GuardianPassword2026"; totp_code=$totp } @("-D", $headers, "-c", $cookies, "-H", "X-Real-IP: attacker-controlled")
  if ($login -notmatch 'e2eadmin') { throw "Login failed" }
  $overview = & curl.exe -kfsS -b $cookies https://localhost:18443/api/v1/overview
  if ($LASTEXITCODE -ne 0 -or $overview -notmatch 'tk_sdn_collator') { throw "Authenticated overview failed" }
  $csrf = ((Get-Content -LiteralPath $headers | Where-Object { $_ -match '^X-CSRF-Token:' }) -replace '^X-CSRF-Token:\s*','').Trim()

	$totp = Get-Totp $setup.secret
	$restartBody = @{ password="E2e!GuardianPassword2026"; totp_code=$totp; reason="E2E signed action boundary verification"; confirm="tk_sdn_collator"; incident_id="" }
	$missingCSRF = Request-Json "POST" "/api/v1/actions/restart" $restartBody @("-b", $cookies, "-H", "Idempotency-Key: e2e-missing-csrf")
	if ($missingCSRF.Status -ne 403) { throw "Restart without CSRF returned $($missingCSRF.Status)" }
	$wrongPasswordBody = $restartBody.Clone()
	$wrongPasswordBody.password = "wrong-password"
	$wrongPassword = Request-Json "POST" "/api/v1/actions/restart" $wrongPasswordBody @("-b", $cookies, "-H", "X-CSRF-Token: $csrf", "-H", "Idempotency-Key: e2e-wrong-password")
	if ($wrongPassword.Status -ne 403) { throw "Restart with a wrong password returned $($wrongPassword.Status)" }
	$wrongTOTPBody = $restartBody.Clone()
	$wrongTOTPBody.totp_code = "000000"
	$wrongTOTP = Request-Json "POST" "/api/v1/actions/restart" $wrongTOTPBody @("-b", $cookies, "-H", "X-CSRF-Token: $csrf", "-H", "Idempotency-Key: e2e-wrong-totp")
	if ($wrongTOTP.Status -ne 403) { throw "Restart with a wrong TOTP returned $($wrongTOTP.Status)" }
	if ((Get-ActionCount) -ne "0") { throw "A failed CSRF/password/TOTP check reached the action socket" }

	& docker @compose --profile tools run --rm --no-deps --build action-probe
	if ($LASTEXITCODE -ne 0) { throw "Unsigned Unix-socket action was not rejected" }
	$totp = Get-Totp $setup.secret
	$restartBody.totp_code = $totp
	$firstAction = Request-Json "POST" "/api/v1/actions/restart" $restartBody @("-b", $cookies, "-H", "X-CSRF-Token: $csrf", "-H", "Idempotency-Key: e2e-idempotency")
	if ($firstAction.Status -ne 202) { throw "Signed action returned $($firstAction.Status): $($firstAction.Body)" }
	$firstActionID = ($firstAction.Body | ConvertFrom-Json).id
	$duplicateAction = Request-Json "POST" "/api/v1/actions/restart" $restartBody @("-b", $cookies, "-H", "X-CSRF-Token: $csrf", "-H", "Idempotency-Key: e2e-idempotency")
	if ($duplicateAction.Status -ne 202 -or ($duplicateAction.Body | ConvertFrom-Json).id -ne $firstActionID) { throw "Identical idempotency retry did not return the existing action" }
	$conflictBody = $restartBody.Clone()
	$conflictBody.reason = "E2E conflicting request must not replace existing action"
	$conflictAction = Request-Json "POST" "/api/v1/actions/restart" $conflictBody @("-b", $cookies, "-H", "X-CSRF-Token: $csrf", "-H", "Idempotency-Key: e2e-idempotency")
	if ($conflictAction.Status -ne 409) { throw "Conflicting idempotency retry returned $($conflictAction.Status)" }
	if ((Get-ActionCount) -ne "1") { throw "Idempotency verification created an unexpected action count" }

	$settings = Request-Json "PUT" "/api/v1/settings" @{ automation_enabled=$false; password="E2e!GuardianPassword2026"; totp_code=(Get-Totp $setup.secret) } @("-b", $cookies, "-H", "X-CSRF-Token: $csrf")
	if ($settings.Status -ne 200) { throw "Broker settings update returned $($settings.Status): $($settings.Body)" }
	$rewardActionsAfter = Get-RewardActionCount
	if ($rewardActionsAfter -ne $rewardActionsBefore) { throw "Reward monitoring created a remediation action: before=$rewardActionsBefore after=$rewardActionsAfter" }
  [void](Post-Json "/api/v1/auth/logout" @{} @("-b", $cookies, "-H", "X-CSRF-Token: $csrf"))
  Write-Host "PASS Compose DB roles + proxy/network/secret isolation + auth + signed action/idempotency E2E"
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
