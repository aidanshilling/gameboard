# Builds GameBoard for Linux and deploys it to the GCE VM.
# Usage (from the repo root):  .\deploy\deploy.ps1
param(
    [string]$Instance = "gameboard",
    [string]$Zone = "us-east1-b",
    [string]$Project = "pingulondotnet"
)
$ErrorActionPreference = "Stop"

$gcloud = (Get-Command gcloud -ErrorAction SilentlyContinue).Source
if (-not $gcloud) { $gcloud = "$env:LOCALAPPDATA\Google\Cloud SDK\google-cloud-sdk\bin\gcloud.cmd" }
if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw "Go is not installed (winget install GoLang.Go)" }

$root = Split-Path $PSScriptRoot -Parent
$out = Join-Path $PSScriptRoot "out"
New-Item -ItemType Directory -Force $out | Out-Null

Write-Host "Building linux/amd64 binary..."
Push-Location $root
try {
    $env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
    go build -trimpath -ldflags="-s -w" -o (Join-Path $out "gameboard") .
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}

$files = @("gameboard.service", "gameboard-backup.service", "gameboard-backup.timer", "Caddyfile", "gameboard-admin", "setup-server.sh") |
    ForEach-Object { Join-Path $PSScriptRoot $_ }
$files += Join-Path $out "gameboard"

$common = @("--zone", $Zone, "--project", $Project, "--quiet")
Write-Host "Uploading..."
& $gcloud compute ssh $Instance @common --command "rm -rf ~/gameboard-deploy && mkdir ~/gameboard-deploy"
if ($LASTEXITCODE -ne 0) { throw "ssh failed" }
# Relative to the remote home dir: on Windows gcloud uses pscp, which doesn't expand "~".
& $gcloud compute scp @files "${Instance}:gameboard-deploy/" @common
if ($LASTEXITCODE -ne 0) { throw "scp failed" }

Write-Host "Installing..."
& $gcloud compute ssh $Instance @common --command "sudo sh ~/gameboard-deploy/setup-server.sh"
if ($LASTEXITCODE -ne 0) { throw "remote setup failed" }
Write-Host "Deployed."
