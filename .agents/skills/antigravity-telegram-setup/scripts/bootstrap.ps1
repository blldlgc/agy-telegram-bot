# Cross-platform Bootstrap Script for Windows (PowerShell)
# This script installs Go and Antigravity CLI if they are missing.

$ErrorActionPreference = "Stop"

Write-Host "Detected Environment: Windows"

# 1. Install Go if missing
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host "Installing Go..."
    $GO_VERSION = "1.22.4"
    $GO_URL = "https://go.dev/dl/go${GO_VERSION}.windows-amd64.zip"
    $TMP_ZIP = "$env:TEMP\go.zip"
    
    Write-Host "Downloading $GO_URL..."
    Invoke-WebRequest -Uri $GO_URL -OutFile $TMP_ZIP
    
    $GO_DEST = "$env:USERPROFILE\go"
    if (Test-Path $GO_DEST) { Remove-Item -Recurse -Force $GO_DEST }
    
    Write-Host "Extracting Go to $GO_DEST..."
    Expand-Archive -Path $TMP_ZIP -DestinationPath $env:USERPROFILE -Force
    Remove-Item $TMP_ZIP
    
    Write-Host "Go installed. Add $GO_DEST\bin to your PATH."
} else {
    Write-Host "Go is already installed."
}

# 2. Install Antigravity CLI (agy) if missing
if (-not (Get-Command agy -ErrorAction SilentlyContinue)) {
    Write-Host "Installing Antigravity CLI..."
    $AGY_URL = "https://antigravity.google/product/antigravity-cli?app=antigravity&os=windows"
    
    $BIN_DIR = "$env:USERPROFILE\.local\bin"
    if (-not (Test-Path $BIN_DIR)) { New-Item -ItemType Directory -Force -Path $BIN_DIR | Out-Null }
    
    $TMP_ZIP = "$env:TEMP\agy.zip"
    Write-Host "Downloading $AGY_URL..."
    
    try {
        Invoke-WebRequest -Uri $AGY_URL -OutFile $TMP_ZIP
        Write-Host "Extracting Antigravity CLI..."
        Expand-Archive -Path $TMP_ZIP -DestinationPath $BIN_DIR -Force
        Remove-Item $TMP_ZIP
    } catch {
        Write-Host "Warning: Download or extraction failed. The agent should handle this manually."
    }
    
    Write-Host "Antigravity CLI installed. Add $BIN_DIR to your PATH."
} else {
    Write-Host "Antigravity CLI is already installed."
}

Write-Host "Bootstrap complete."
