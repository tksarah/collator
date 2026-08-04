param(
  [string]$SshTarget = "shiden-collator",
  [string]$RemoteRoot = "/home/tk/shiden-guardian"
)
$ErrorActionPreference = "Stop"
$repo = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$version = (Get-Date).ToUniversalTime().ToString("yyyyMMddTHHmmssZ")
$releaseDir = Join-Path $repo "release"
$archive = Join-Path $releaseDir "shiden-guardian-$version.tar.gz"
$checksumFile = "$archive.sha256"
New-Item -ItemType Directory -Path $releaseDir -Force | Out-Null

Write-Host "Read-only SSH preflight..."
Get-Content -LiteralPath (Join-Path $repo "scripts\preflight.sh") -Raw | & ssh -o BatchMode=yes -o StrictHostKeyChecking=yes $SshTarget "sh -s"
if ($LASTEXITCODE -gt 2) { throw "SSH access or remote preflight failed." }

Write-Host "Local locked builds and tests..."
Push-Location $repo
try {
  & corepack pnpm install --frozen-lockfile
  if ($LASTEXITCODE -ne 0) { throw "pnpm install failed." }
  & corepack pnpm test
  if ($LASTEXITCODE -ne 0) { throw "Web build/test failed." }
  $env:ENV_FILE = ".env.example"
  & docker compose --env-file .env.example config --quiet
  if ($LASTEXITCODE -ne 0) { throw "Compose validation failed." }
  & docker buildx build --platform linux/amd64 --target api --file deploy/Dockerfile.backend .
  if ($LASTEXITCODE -ne 0) { throw "Linux Go build/test failed." }
  & docker buildx build --platform linux/amd64 --file deploy/Dockerfile.web --build-arg NEXT_PUBLIC_SITE_URL=https://guardian.invalid .
  if ($LASTEXITCODE -ne 0) { throw "Web container build failed." }
  & tar.exe --exclude=.git --exclude=node_modules --exclude=.next --exclude=out --exclude=.pnpm-store --exclude=.npm-cache --exclude=.gocache --exclude=.env --exclude=secrets --exclude=backups --exclude=release -czf $archive .
  if ($LASTEXITCODE -ne 0) { throw "Archive creation failed." }
} finally {
  Remove-Item Env:ENV_FILE -ErrorAction SilentlyContinue
  Pop-Location
}
$checksum = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant()
Set-Content -LiteralPath $checksumFile -Value "$checksum  source.tar.gz" -Encoding ascii -NoNewline

& ssh $SshTarget "mkdir -p '$RemoteRoot/releases/$version'"
if ($LASTEXITCODE -ne 0) { throw "Remote release directory creation failed." }
& scp $archive $checksumFile "${SshTarget}:$RemoteRoot/releases/$version/"
if ($LASTEXITCODE -ne 0) { throw "Upload failed." }
$remote = "cd '$RemoteRoot/releases/$version' && mv 'shiden-guardian-$version.tar.gz' source.tar.gz && mv 'shiden-guardian-$version.tar.gz.sha256' source.tar.gz.sha256 && sha256sum -c source.tar.gz.sha256 && tar -xzf source.tar.gz && sh scripts/preflight.sh"
& ssh $SshTarget $remote
if ($LASTEXITCODE -gt 2) { throw "Remote checksum, extraction, or preflight failed." }
& ssh $SshTarget "cd '$RemoteRoot/releases/$version' && sh scripts/build-agent.sh"
if ($LASTEXITCODE -ne 0) { throw "Remote agent build failed." }

Write-Host "`nRelease staged and agent artifact built: $RemoteRoot/releases/$version"
Write-Host "Keep your existing password-authenticated SSH session open. Review the files, then run there:"
Write-Host "  cd '$RemoteRoot/releases/$version'"
Write-Host "  less scripts/bootstrap-host.sh deploy/shiden-guardian-agent.service deploy/sudoers-shiden-guardian"
Write-Host "  sudo sh scripts/bootstrap-host.sh"
Write-Host "  sh scripts/configure-release.sh"
Write-Host "`nAfter that one-time interactive step, activate from Windows without a PTY:"
Write-Host ('  ssh {0} "sh ''{1}/releases/{2}/scripts/activate-release.sh'' ''{1}'' ''{2}''"' -f $SshTarget, $RemoteRoot, $version)
