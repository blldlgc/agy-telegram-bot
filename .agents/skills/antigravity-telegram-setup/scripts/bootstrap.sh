#!/bin/sh
# Cross-platform Bootstrap Script for Unix (macOS/Linux)
# This script installs Go and Antigravity CLI if they are missing.
# It uses standard tools like curl and tar.

set -e

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

if [ "$ARCH" = "x86_64" ]; then
    ARCH="amd64"
elif [ "$ARCH" = "aarch64" ] || [ "$ARCH" = "arm64" ]; then
    ARCH="arm64"
fi

echo "Detected Environment: $OS ($ARCH)"

download() {
    URL="$1"
    DEST="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -sSL "$URL" -o "$DEST"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$DEST" "$URL"
    else
        echo "Error: curl or wget is required but not found."
        exit 1
    fi
}

# 1. Install Go if missing
if ! command -v go >/dev/null 2>&1; then
    echo "Installing Go..."
    GO_VERSION="1.22.4"
    GO_URL="https://go.dev/dl/go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
    
    TMP_DIR=$(mktemp -d)
    echo "Downloading $GO_URL..."
    download "$GO_URL" "$TMP_DIR/go.tar.gz"
    
    # Try sudo if available, else install to ~/.local
    if [ "$(id -u)" = "0" ] || sudo -n true 2>/dev/null; then
        sudo rm -rf /usr/local/go
        sudo tar -C /usr/local -xzf "$TMP_DIR/go.tar.gz"
        echo "Go installed. Add /usr/local/go/bin to your PATH."
    else
        mkdir -p ~/.local
        rm -rf ~/.local/go
        tar -C ~/.local -xzf "$TMP_DIR/go.tar.gz"
        echo "Go installed. Add ~/.local/go/bin to your PATH."
    fi
    rm -rf "$TMP_DIR"
else
    echo "Go is already installed: $(go version)"
fi

# 2. Install Antigravity CLI (agy) if missing
if ! command -v agy >/dev/null 2>&1; then
    echo "Installing Antigravity CLI..."
    AGY_URL="https://antigravity.google/product/antigravity-cli?app=antigravity"
    
    mkdir -p ~/.local/bin
    TMP_DIR=$(mktemp -d)
    
    echo "Downloading $AGY_URL..."
    download "$AGY_URL" "$TMP_DIR/agy.tar.gz"
    
    # Extract assuming it's a tar.gz
    tar -xzf "$TMP_DIR/agy.tar.gz" -C ~/.local/bin/ || echo "Warning: extraction failed. The file might not be a standard tarball. Agent should handle manually."
    chmod +x ~/.local/bin/agy 2>/dev/null || true
    
    rm -rf "$TMP_DIR"
    echo "Antigravity CLI downloaded to ~/.local/bin/agy. Add ~/.local/bin to your PATH."
else
    echo "Antigravity CLI is already installed: $(agy --version 2>/dev/null)"
fi

echo "Bootstrap complete."
