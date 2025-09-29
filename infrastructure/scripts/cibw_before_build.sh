#!/usr/bin/env bash
set -euo pipefail

echo "[cibw_before_build] Starting pre-build for Codex binary"

# Detect OS/ARCH first, so we can early-exit if a reused binary already exists
OS="${OS:-$(uname -s)}"
ARCH="${ARCH:-$(uname -m)}"

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

PACKAGE_ROOT="omnara"
if [[ -d "src/omnara" ]]; then
  PACKAGE_ROOT="src/omnara"
fi

DEST_DIR="${PACKAGE_ROOT}/_bin/codex/${ARCH_TAG}"
DEST="${DEST_DIR}/codex${BIN_EXT}"

# If a binary is already present (e.g., fetched from a previous release), reuse it
if [[ -f "$DEST" ]]; then
  echo "[cibw_before_build] Reusing pre-populated Codex binary at ${DEST}"
  if [[ -z "$BIN_EXT" ]]; then
    chmod +x "${DEST}" || true
  fi
  echo "[cibw_before_build] Done (reused)"
  exit 0
fi

# If Codex hasn't changed and we're NOT on Linux, reuse previous wheel's binary
CODEX_CHANGED="${CODEX_CHANGED:-}"
CODEX_PREV_TAG="${CODEX_PREV_TAG:-}"
if [[ "$OS" != "Linux" && "$CODEX_CHANGED" == "false" && -n "$CODEX_PREV_TAG" ]]; then
  echo "[cibw_before_build] Attempting reuse from previous release: ${CODEX_PREV_TAG} (${ARCH_TAG})"
  PREV_VERSION="${CODEX_PREV_TAG#v}"
  mkdir -p prior_wheels "$DEST_DIR"
  if python -m pip download --no-deps --only-binary=:all: --dest prior_wheels "omnara==${PREV_VERSION}"; then
    WHEEL=$(ls -1 prior_wheels/*.whl 2>/dev/null | head -n1 || true)
    if [[ -n "$WHEEL" ]]; then
      echo "[cibw_before_build] Extracting binary from $WHEEL"
      export WHEEL="$WHEEL"
      export ARCH_TAG="$ARCH_TAG"
      export DEST="$DEST"
      python - <<'PY'
import os, zipfile, sys, stat

wheel = os.environ.get('WHEEL')
arch_tag = os.environ.get('ARCH_TAG')
dest = os.environ.get('DEST')
if not all([wheel, arch_tag, dest]):
    sys.exit(0)

candidate_paths = [
    f"omnara/_bin/codex/{arch_tag}/codex",
    f"omnara/_bin/codex/{arch_tag}/codex.exe",
]

with zipfile.ZipFile(wheel) as z:
    names = set(z.namelist())
    found = None
    for c in candidate_paths:
        if c in names:
            found = c
            break
    if not found:
        sys.exit(0)
    os.makedirs(os.path.dirname(dest), exist_ok=True)
    with z.open(found) as src, open(dest, 'wb') as dst:
        dst.write(src.read())
    if not dest.endswith('.exe'):
        try:
            mode = os.stat(dest).st_mode
            os.chmod(dest, mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
        except Exception:
            pass
print('[cibw_before_build] Reused codex binary at', dest)
PY
      if [[ -f "$DEST" ]]; then
        echo "[cibw_before_build] Done (reused from previous wheel)"
        exit 0
      fi
    else
      echo "[cibw_before_build] No wheel downloaded to prior_wheels; will build instead"
    fi
  else
    echo "[cibw_before_build] pip download failed; will build instead"
  fi
fi

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

# On Linux, ensure OpenSSL headers and pkg-config are available
if [[ "$OS" == "Linux" ]]; then
  echo "[cibw_before_build] Installing OpenSSL runtime+headers and pkg-config on Linux"
  if command -v yum >/dev/null 2>&1; then
    yum -y install openssl openssl-libs openssl-devel pkgconfig zlib-devel || true
  elif command -v microdnf >/dev/null 2>&1; then
    microdnf -y install openssl openssl-libs openssl-devel pkgconfig zlib-devel || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf -y install openssl openssl-libs openssl-devel pkgconfig zlib-devel || true
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update && apt-get install -y libssl-dev pkg-config zlib1g-dev || true
  fi
fi

# Build codex-cli (Rust) in release mode as a fallback
echo "[cibw_before_build] Building codex-cli (fallback build)"
pushd integrations/cli_wrappers/codex/codex-rs >/dev/null

cargo build --release -p codex-cli

popd >/dev/null

# Compute path to built binary (absolute or rooted at repo)
SRC="integrations/cli_wrappers/codex/codex-rs/target/release/codex${BIN_EXT}"

# Install built binary into wheel payload
echo "[cibw_before_build] Installing Codex binary to ${DEST}"
mkdir -p "${DEST_DIR}"
cp "${SRC}" "${DEST}"
if [[ -z "$BIN_EXT" ]]; then
  chmod +x "${DEST}"
fi

echo "[cibw_before_build] Done"
