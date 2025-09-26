"""Helpers for streaming local agent terminals through the relay."""

from __future__ import annotations

import fcntl
import os
import pty
import select
import signal
import socket
import struct
import sys
import termios
import tty
import uuid
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from shutil import get_terminal_size, which
from typing import Iterable, List, Optional

import paramiko


LOG_FILE = Path(__file__).resolve().parents[1] / "logs" / "session_sharing.log"

FRAME_HEADER = struct.Struct("!BI")
FRAME_TYPE_OUTPUT = 0
FRAME_TYPE_INPUT = 1
FRAME_TYPE_RESIZE = 2
RESIZE_PAYLOAD = struct.Struct("!HH")


def _pack_frame(frame_type: int, payload: bytes) -> bytes:
    return FRAME_HEADER.pack(frame_type, len(payload)) + payload


def _log(message: str) -> None:
    """Log helper that writes to stdout and a shared log file."""
    try:
        LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        timestamp = datetime.utcnow().isoformat()
        with LOG_FILE.open("a", encoding="utf-8") as fh:
            fh.write(f"{timestamp} {message}\n")
    except Exception:
        # Failing to write logs should never break the session wrapper
        pass


@dataclass(slots=True)
class RelayClientSettings:
    """Configuration for the client-side relay connection."""

    host: str = field(
        default_factory=lambda: os.getenv("OMNARA_RELAY_HOST", "relay.omnara.com")
    )
    port: int = field(
        default_factory=lambda: int(os.getenv("OMNARA_RELAY_PORT", "2222"))
    )
    term: str = field(
        default_factory=lambda: os.environ.get("TERM", "xterm-256color")
    )
    enabled: bool = field(
        default_factory=lambda: os.getenv("OMNARA_RELAY_DISABLED", "0")
        not in {"1", "true", "TRUE"}
    )


AGENT_EXECUTABLE = {
    "claude": "claude",
    "amp": "amp",
}


def build_agent_command(
    agent: str, args, unknown_args: Optional[Iterable[str]], api_key: str
) -> List[str]:
    """Build the subprocess command used to launch the underlying agent."""

    executable = AGENT_EXECUTABLE.get(agent)
    if not executable:
        raise ValueError(f"Relay does not support agent '{agent}'")

    if which(executable) is None:
        raise RuntimeError(
            f"Could not locate '{executable}' on PATH. Please install it or adjust PATH."
        )

    command: List[str] = [executable]

    if unknown_args:
        command.extend(list(unknown_args))

    return command


def run_agent_with_relay(agent: str, args, unknown_args: Optional[Iterable[str]], api_key: str) -> int:
    """Launch the agent CLI and mirror its terminal through the relay."""

    settings = RelayClientSettings()
    command = build_agent_command(agent, args, unknown_args, api_key)

    session_id = str(uuid.uuid4())[:8]
    relay_username = session_id

    ssh_client: Optional[paramiko.SSHClient] = None
    channel = None
    last_window: Optional[tuple[int, int]] = None  # (cols, rows)

    if settings.enabled:
        ssh_client = paramiko.SSHClient()
        ssh_client.set_missing_host_key_policy(paramiko.AutoAddPolicy())

        try:
            _log(
                f"[DEBUG] Connecting to relay ssh://{settings.host}:{settings.port}"
            )
            ssh_client.connect(
                hostname=settings.host,
                port=settings.port,
                username=relay_username,
                password=api_key,
                allow_agent=False,
                look_for_keys=False,
                timeout=10,
            )
            channel = ssh_client.invoke_shell(term=settings.term)
            channel.settimeout(0.0)
            _log(f"[DEBUG] Relay channel established session_id={session_id}")
        except Exception as exc:
            _log(
                f"[WARN] Unable to reach relay at {settings.host}:{settings.port}: {exc!r}"
                "\n       Continuing locally without session sharing."
            )
            channel = None
            if ssh_client:
                ssh_client.close()
                ssh_client = None

    child_env = os.environ.copy()
    child_env.setdefault("OMNARA_SESSION_ID", session_id)
    child_env.setdefault("OMNARA_API_KEY", api_key)
    if getattr(args, "base_url", None):
        child_env.setdefault("OMNARA_API_URL", args.base_url)

    child_pid, master_fd = pty.fork()

    if child_pid == 0:
        # Child process: replace with the agent executable.
        try:
            os.execvpe(command[0], command, child_env)
        except Exception as exc:  # pragma: no cover - crash paths
            _log(f"[ERROR] Failed to exec agent process: {exc!r}")
            os._exit(1)

    stdin_fd = None
    tty_state = None
    sigwinch_prev_handler = None

    def _record_window_size(cols: int, rows: int) -> None:
        nonlocal last_window
        last_window = (cols, rows)

    def _send_resize_frame() -> None:
        if channel is None or last_window is None:
            return

        cols, rows = last_window
        payload = RESIZE_PAYLOAD.pack(rows, cols)
        frame = _pack_frame(FRAME_TYPE_RESIZE, payload)
        try:
            channel.sendall(frame)
            _log(
                f"[TRACE] sent resize cols={cols} rows={rows} (session_id={session_id})"
            )
        except Exception as exc:
            _log(
                f"[WARN] Relay resize send failed session_id={session_id} exc={exc!r}"
            )

    def _apply_window_size() -> None:
        try:
            cols, rows = get_terminal_size(fallback=(80, 24))
        except OSError:
            cols, rows = (80, 24)

        winsize = struct.pack("HHHH", rows, cols, 0, 0)
        try:
            fcntl.ioctl(master_fd, termios.TIOCSWINSZ, winsize)
        except Exception:
            pass

        _record_window_size(cols, rows)
        _send_resize_frame()

    if sys.stdin and sys.stdin.isatty():
        stdin_fd = sys.stdin.fileno()
        try:
            tty_state = termios.tcgetattr(stdin_fd)
            tty.setraw(stdin_fd)
        except Exception:
            tty_state = None

        _apply_window_size()

        def _on_sigwinch(signum, frame):  # type: ignore[override]
            _apply_window_size()

        try:
            sigwinch_prev_handler = signal.signal(signal.SIGWINCH, _on_sigwinch)
        except Exception:
            sigwinch_prev_handler = None
    else:
        _apply_window_size()

    if channel is not None:
        _send_resize_frame()

    try:
        flags = fcntl.fcntl(master_fd, fcntl.F_GETFL)
        fcntl.fcntl(master_fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)
    except Exception:
        pass

    exit_status: Optional[int] = None

    if channel is not None:
        channel_fd = channel.fileno()
        channel.settimeout(0.0)
    else:
        channel_fd = None

    channel_buffer = bytearray()

    def _send_to_channel(data: bytes) -> None:
        if channel is None:
            return

        frame = _pack_frame(FRAME_TYPE_OUTPUT, data)
        try:
            channel.sendall(frame)
            _log(
                f"[TRACE] upstream chunk len={len(data)} sample={data[:32]!r} (session_id={session_id})"
            )
        except Exception as exc:
            _log(
                f"[WARN] Relay send failed session_id={session_id} exc={exc!r}"
            )

    try:
        while True:
            fds = [master_fd]
            if channel_fd is not None:
                fds.append(channel_fd)
            if stdin_fd is not None:
                fds.append(stdin_fd)

            ready, _, _ = select.select(fds, [], [], 0.1)

            if master_fd in ready:
                data = os.read(master_fd, 8192)
                if not data:
                    break
                os.write(sys.stdout.fileno(), data)
                _send_to_channel(data)

            if channel_fd is not None and channel_fd in ready:
                try:
                    data = channel.recv(8192)
                except socket.timeout:
                    data = b""
                except Exception as exc:
                    _log(
                        f"[WARN] Relay receive failed session_id={session_id} exc={exc!r}"
                    )
                    channel_fd = None
                    channel = None
                    data = b""

                if data:
                    if isinstance(data, str):
                        chunk_bytes = data.encode()
                    else:
                        chunk_bytes = data
                    channel_buffer.extend(chunk_bytes)
                    _log(
                        f"[TRACE] raw downstream len={len(chunk_bytes)} sample={chunk_bytes[:32]!r} (session_id={session_id})"
                    )

                    while True:
                        if len(channel_buffer) < FRAME_HEADER.size:
                            break

                        frame_type, frame_len = FRAME_HEADER.unpack(
                            channel_buffer[: FRAME_HEADER.size]
                        )
                        total_len = FRAME_HEADER.size + frame_len
                        if len(channel_buffer) < total_len:
                            break

                        payload = bytes(channel_buffer[FRAME_HEADER.size:total_len])
                        del channel_buffer[:total_len]

                        if frame_type == FRAME_TYPE_INPUT:
                            _log(
                                f"[TRACE] decoded downstream len={len(payload)} sample={payload[:32]!r} (session_id={session_id})"
                            )
                            for offset in range(0, len(payload), 1024):
                                chunk = payload[offset : offset + 1024]
                                while True:
                                    try:
                                        os.write(master_fd, chunk)
                                        break
                                    except BlockingIOError:
                                        select.select([], [master_fd], [], 0.1)
                                    except InterruptedError:
                                        continue
                        else:
                            _log(
                                f"[WARN] Ignoring frame type {frame_type} len={frame_len} (session_id={session_id})"
                            )

            if stdin_fd is not None and stdin_fd in ready:
                data = os.read(stdin_fd, 8192)
                if not data:
                    stdin_fd = None
                else:
                    for offset in range(0, len(data), 1024):
                        chunk = data[offset : offset + 1024]
                        while True:
                            try:
                                os.write(master_fd, chunk)
                                break
                            except BlockingIOError:
                                select.select([], [master_fd], [], 0.1)
                            except InterruptedError:
                                continue

                    _send_to_channel(data)

            try:
                finished_pid, status = os.waitpid(child_pid, os.WNOHANG)
            except ChildProcessError:
                finished_pid = child_pid
                status = 0

            if finished_pid == child_pid:
                exit_status = status
                exit_code = (
                    os.WEXITSTATUS(status) if os.WIFEXITED(status) else 1
                )
                _log(
                    f"[INFO] Agent process exited with code {exit_code} (session_id={session_id})"
                )
                break

    except KeyboardInterrupt:
        pass
    finally:
        if tty_state is not None and stdin_fd is not None:
            termios.tcsetattr(stdin_fd, termios.TCSADRAIN, tty_state)
        if sigwinch_prev_handler is not None:
            try:
                signal.signal(signal.SIGWINCH, sigwinch_prev_handler)
            except Exception:
                pass

        os.close(master_fd)

        if channel is not None:
            try:
                channel.close()
            except Exception:
                pass

        if ssh_client is not None:
            ssh_client.close()

        # Ensure the child is terminated if it's still running.
        if exit_status is None:
            try:
                os.kill(child_pid, signal.SIGTERM)
            except Exception:
                pass

            try:
                finished_pid, exit_status = os.waitpid(child_pid, 0)
                if finished_pid != child_pid:
                    exit_status = 0
            except Exception:
                exit_status = 0

    if exit_status is None:
        return 0

    if os.WIFEXITED(exit_status):
        return os.WEXITSTATUS(exit_status)

    return 1
