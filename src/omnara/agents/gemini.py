import os
import platform
import shutil
import subprocess
import sys
import uuid
import threading
import time
from pathlib import Path
from typing import Optional

from omnara.sdk.client import OmnaraClient


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
        arch = machine or "unknown"
        ext = ""
        tag = f"{system.lower()}-{arch}"
    return tag, ext, system


def _packaged_binary_path() -> Path:
    """Return packaged Gemini binary path inside the wheel, if present."""
    tag, ext, _ = _platform_tag()
    base = Path(__file__).resolve().parent.parent / "_bin" / "gemini" / tag
    return base / f"gemini{ext}"


def _env_binary_path() -> Optional[Path]:
    """Return a path from OMNARA_GEMINI_PATH if set.

    Accepts either a direct file path to the binary or a directory, in which case
    we append the platform-specific binary name (gemini[.exe]).
    """
    p = os.environ.get("OMNARA_GEMINI_PATH")
    if not p:
        return None
    p = os.path.expanduser(p)
    path = Path(p)
    if path.is_dir():
        tag, ext, _ = _platform_tag()
        return path / f"gemini{ext}"
    return path


def _path_binary_path() -> Optional[Path]:
    """Lookup 'gemini' on PATH as a convenience for local installs."""
    which = shutil.which("gemini")
    if which:
        return Path(which)
    # Also check a few common locations
    candidates = [
        Path.home() / ".npm-global/bin/gemini",
        Path("/usr/local/bin/gemini"),
        Path.home() / ".local/bin/gemini",
        Path.home() / "node_modules/.bin/gemini",
        Path.home() / ".yarn/bin/gemini",
    ]
    for c in candidates:
        if c.exists() and c.is_file():
            return c
    return None


def _resolve_gemini_binary() -> Path:
    # 1) explicit override via env var
    env_p = _env_binary_path()
    if env_p and env_p.exists():
        return env_p

    # 2) packaged in the wheel (optional)
    packaged = _packaged_binary_path()
    if packaged.exists():
        return packaged

    # 3) found on PATH or common locations
    path_p = _path_binary_path()
    if path_p and path_p.exists():
        return path_p

    raise FileNotFoundError(
        "Gemini CLI not found.\n"
        "Install the 'gemini' CLI (e.g. v0.6.1) and ensure it is on PATH,\n"
        "or set OMNARA_GEMINI_PATH to the binary file or its containing directory."
    )


def run_gemini(args, unknown_args, api_key: str):
    """Launch Gemini with Omnara integration.

    Preferred: delegate to the Python wrapper (captures messages and syncs to Omnara).
    Fallback: run the raw `gemini` binary with minimal env + heartbeat.
    """
    # First try the full-featured Python wrapper which handles message syncing
    try:
        import importlib

        module = importlib.import_module(
            "integrations.cli_wrappers.gemini.gemini_wrapper"
        )
        wrapper_main = getattr(module, "main")

        # Prepare argv for the wrapper
        original_argv = sys.argv
        new_argv = [
            "gemini_wrapper",
            "--api-key",
            api_key,
        ]
        base_url = getattr(args, "base_url", None)
        if base_url:
            new_argv.extend(["--base-url", base_url])
        if unknown_args:
            new_argv.extend(unknown_args)

        # Ensure session id is stable and discoverable
        os.environ.setdefault("OMNARA_SESSION_ID", str(uuid.uuid4()))
        os.environ["OMNARA_AGENT_INSTANCE_ID"] = os.environ["OMNARA_SESSION_ID"]
        # Do not force pipe mode; allow wrapper defaults (PTY) for interactive UI

        try:
            sys.argv = new_argv
            return wrapper_main()
        finally:
            sys.argv = original_argv
    except Exception:
        # Wrapper import failed; fallback to raw binary + heartbeat
        pass

    # Fallback path mirrors Codex-style bare launcher
    try:
        bin_path = _resolve_gemini_binary()
    except FileNotFoundError as e:
        print(f"[ERROR] {e}")
        sys.exit(1)

    env = os.environ.copy()
    env["OMNARA_API_KEY"] = api_key

    base_url = getattr(args, "base_url", None) or env.get("OMNARA_API_URL")
    if base_url:
        env["OMNARA_API_URL"] = base_url
    session_id = env.setdefault("OMNARA_SESSION_ID", str(uuid.uuid4()))
    env["OMNARA_AGENT_INSTANCE_ID"] = session_id

    try:
        if bin_path.is_file() and os.name != "nt":
            mode = os.stat(bin_path).st_mode
            if (mode & 0o111) == 0:
                os.chmod(bin_path, mode | 0o111)
    except Exception:
        pass

    cmd = [str(bin_path)]
    if unknown_args:
        cmd.extend(unknown_args)

    # Minimal heartbeat so the instance appears even without wrapper
    stop_event = threading.Event()

    def _heartbeat_loop():
        try:
            client = OmnaraClient(api_key=api_key, base_url=(base_url or "https://agent.omnara.com"))
            session = client.session
            url = (base_url or "https://agent.omnara.com").rstrip("/") + f"/api/v1/agents/instances/{session_id}/heartbeat"
            import random
            time.sleep(random.uniform(0, 2.0))
            while not stop_event.is_set():
                try:
                    _ = session.post(url, timeout=10)
                except Exception:
                    pass
                delay = 30.0 + random.uniform(-2.0, 2.0)
                if delay < 5:
                    delay = 5
                end = time.time() + delay
                while time.time() < end and not stop_event.is_set():
                    time.sleep(0.1)
        except Exception:
            pass

    hb_thread = threading.Thread(target=_heartbeat_loop, daemon=True)
    hb_thread.start()

    try:
        subprocess.run(cmd, env=env, check=False)
    except KeyboardInterrupt:
        sys.exit(130)
    finally:
        stop_event.set()
        try:
            hb_thread.join(timeout=2.0)
        except Exception:
            pass
