import os
import platform
import subprocess
import sys
import uuid
from pathlib import Path


def _platform_tag() -> tuple[str, str, str]:
    system = platform.system()
    machine = platform.machine().lower()

    if system == "Darwin":
        arch = "arm64" if machine in ("arm64", "aarch64") else "x64"
        ext = ""
        tag = f"darwin-{arch}"
    elif system == "Linux":
        arch = "x64" if machine in ("x86_64", "amd64") else machine
        ext = ""
        tag = f"linux-{arch}"
    elif system == "Windows":
        arch = "x64" if machine in ("amd64", "x86_64") else machine
        ext = ".exe"
        tag = f"win-{arch}"
    else:
        # Fallback for unknown
        arch = machine or "unknown"
        ext = ""
        tag = f"{system.lower()}-{arch}"
    return tag, ext, system


def _packaged_binary_path() -> Path:
    """Return packaged binary path inside the wheel, if present."""
    tag, ext, _ = _platform_tag()
    base = Path(__file__).resolve().parent.parent / "_bin" / "codex" / tag
    return base / f"codex{ext}"


def _dev_binary_path() -> Path:
    """Return dev fallback path (after local cargo build)."""
    tag, ext, _ = _platform_tag()
    # The dev path is platform-independent; we only use `ext` for Windows
    root = Path(__file__).resolve().parents[2]
    return (
        root / "integrations/cli_wrappers/codex/codex-rs/target/release" / f"codex{ext}"
    )


def _resolve_codex_binary() -> Path:
    packaged = _packaged_binary_path()
    if packaged.exists():
        return packaged
    dev = _dev_binary_path()
    if dev.exists():
        return dev
    raise FileNotFoundError(
        "Codex binary not found. Expected a packaged binary in the wheel at "
        f"{_packaged_binary_path()} or a locally built dev binary at {_dev_binary_path()}. "
        "To build locally: `cd integrations/cli_wrappers/codex/codex-rs && cargo build --release -p cli`."
    )


def run_codex(args, unknown_args, api_key: str):
    try:
        bin_path = _resolve_codex_binary()
    except FileNotFoundError as e:
        print(f"[ERROR] {e}")
        sys.exit(1)
    env = os.environ.copy()

    # Wire Omnara env for the Rust client
    env["OMNARA_API_KEY"] = api_key
    if getattr(args, "base_url", None):
        env["OMNARA_API_URL"] = args.base_url
    env.setdefault("OMNARA_SESSION_ID", str(uuid.uuid4()))

    # Ensure executable bit if running from packaged file on Unix
    try:
        if bin_path.is_file() and os.name != "nt":
            mode = os.stat(bin_path).st_mode
            # 0o111 owner/group/other execute bits
            if (mode & 0o111) == 0:
                os.chmod(bin_path, mode | 0o111)
    except Exception:
        pass

    cmd = [str(bin_path)]
    if unknown_args:
        cmd.extend(unknown_args)

    try:
        subprocess.run(cmd, env=env, check=False)
    except KeyboardInterrupt:
        sys.exit(130)
