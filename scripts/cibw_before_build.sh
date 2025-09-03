#!/usr/bin/env bash
set -euo pipefail

echo "[cibw_before_build] Starting pre-build for Codex binary"

# Ensure curl exists (manylinux images typically have it; add fallback)
if ! command -v curl >/dev/null 2>&1; then
  echo "[cibw_before_build] curl not found; attempting to install (yum/apk)" >&2
  if command -v yum >/dev/null 2>&1; then
    yum -y install curl || true
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache curl || true
  fi
fi

# Install Rust toolchain (if not already present)
if [ ! -x "$HOME/.cargo/bin/cargo" ]; then
  echo "[cibw_before_build] Installing Rust via rustup"
  curl https://sh.rustup.rs -sSf | sh -s -- -y
fi
source "$HOME/.cargo/env"
rustc -V || true
cargo -V || true

# Build codex-cli (Rust) in release mode
echo "[cibw_before_build] Building codex-cli"
pushd integrations/cli_wrappers/codex/codex-rs >/dev/null
cargo build --release -p codex-cli
popd >/dev/null

# Determine platform arch tag for placement
OS="$(uname -s)"
ARCH="$(uname -m)"
ARCH_TAG=""
BIN_EXT=""
case "$OS" in
  Darwin)
    if [[ "$ARCH" == "arm64" || "$ARCH" == "aarch64" ]]; then
      ARCH_TAG="darwin-arm64"
    else
      ARCH_TAG="darwin-x64"
    fi
    ;;
  Linux)
    ARCH_TAG="linux-x64"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    ARCH_TAG="win-x64"
    BIN_EXT=".exe"
    ;;
  *)
    echo "[cibw_before_build] Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

SRC="integrations/cli_wrappers/codex/codex-rs/target/release/codex${BIN_EXT}"
DEST_DIR="omnara/_bin/codex/${ARCH_TAG}"
DEST="${DEST_DIR}/codex${BIN_EXT}"
echo "[cibw_before_build] Installing Codex binary to ${DEST}"
mkdir -p "${DEST_DIR}"
cp "${SRC}" "${DEST}"
if [[ -z "$BIN_EXT" ]]; then
  chmod +x "${DEST}"
fi

echo "[cibw_before_build] Done"

