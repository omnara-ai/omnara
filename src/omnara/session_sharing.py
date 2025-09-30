"""Helpers for streaming local agent terminals through the relay."""

from __future__ import annotations

import fcntl
import json
import os
import pty
import select
import signal
import socket
import ssl
import struct
import sys
import termios
import tty
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from shutil import get_terminal_size, which
from typing import Iterable, List, Optional
from urllib.parse import urlencode

import websocket  # type: ignore[import-untyped]
from websocket import (  # type: ignore[import-untyped]
    WebSocketConnectionClosedException,
    WebSocketTimeoutException,
)

from omnara.sdk.client import OmnaraClient
from omnara.sdk.exceptions import APIError, AuthenticationError, TimeoutError


LOG_FILE = Path(__file__).resolve().parents[1] / "logs" / "session_sharing.log"

FRAME_HEADER = struct.Struct("!BI")
FRAME_TYPE_OUTPUT = 0
FRAME_TYPE_INPUT = 1
FRAME_TYPE_RESIZE = 2
RESIZE_PAYLOAD = struct.Struct("!HH")


def _is_truthy(value: str | None) -> bool:
    if value is None:
        return False
    return value.strip().lower() in {"1", "true", "yes", "on"}


class WebSocketChannelAdapter:
    """Adapter exposing websocket operations with a Paramiko-like API."""

    def __init__(self, ws: "websocket.WebSocket") -> None:  # type: ignore[name-defined]
        self._ws = ws

    def fileno(self) -> int:
        return self._ws.sock.fileno()

    def settimeout(self, timeout: float) -> None:
        self._ws.settimeout(timeout)

    def sendall(self, data: bytes) -> None:
        self._ws.send_binary(data)

    def recv(self, _bufsize: int) -> bytes:
        try:
            data = self._ws.recv()
        except WebSocketTimeoutException as exc:  # type: ignore[misc]
            raise socket.timeout() from exc
        except WebSocketConnectionClosedException as exc:  # type: ignore[misc]
            raise OSError("websocket closed") from exc

        if data is None:
            return b""
        if isinstance(data, str):
            return data.encode()
        return data

    def close(self) -> None:
        try:
            self._ws.close()
        except Exception:
            pass


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

    relay_url: str = field(
        default_factory=lambda: os.getenv(
            "OMNARA_RELAY_URL", "wss://relay.omnara.com/agent"
        )
    )
    enabled: bool = field(
        default_factory=lambda: not _is_truthy(os.getenv("OMNARA_RELAY_DISABLED"))
    )
    ws_skip_verify: bool = field(
        default_factory=lambda: _is_truthy(os.getenv("OMNARA_RELAY_WS_SKIP_VERIFY"))
    )


AGENT_EXECUTABLE = {
    "claude": "claude",
    "amp": "amp",
    "codex": "codex",
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


def _connect_websocket_channel(
    settings: RelayClientSettings,
    session_id: str,
    api_key: str,
    relay_log_id: str,
) -> Optional[WebSocketChannelAdapter]:
    """Establish a websocket connection to the relay agent endpoint."""

    # Parse the relay URL and add session_id query parameter
    from urllib.parse import urlparse, urlunparse, parse_qs

    parsed = urlparse(settings.relay_url)
    query_params = parse_qs(parsed.query)
    query_params["session_id"] = [session_id]
    query = urlencode(query_params, doseq=True)

    url = urlunparse(
        (
            parsed.scheme,
            parsed.netloc,
            parsed.path,
            parsed.params,
            query,
            parsed.fragment,
        )
    )

    headers = [f"X-API-Key: {api_key}"]
    connect_kwargs: dict[str, object] = {
        "header": headers,
        "enable_multithread": True,
    }
    if parsed.scheme == "wss" and settings.ws_skip_verify:
        connect_kwargs["sslopt"] = {"cert_reqs": ssl.CERT_NONE}

    _log(f"[DEBUG] Connecting to relay websocket {url} (instance_id={relay_log_id})")

    try:
        ws = websocket.create_connection(url, **connect_kwargs)
    except Exception as exc:
        _log(
            f"[WARN] Unable to reach relay websocket {url}: {exc!r}\n"
            "       Continuing locally without session sharing."
        )
        return None

    try:
        ready = ws.recv()
        if isinstance(ready, bytes):
            ready_text = ready.decode("utf-8", "ignore")
        else:
            ready_text = ready
        payload = json.loads(ready_text)
        if payload.get("type") != "ready":
            raise ValueError(f"unexpected response: {payload}")
    except Exception as exc:
        _log(
            f"[WARN] Relay websocket handshake failed instance_id={relay_log_id}: {exc!r}"
        )
        try:
            ws.close()
        except Exception:
            pass
        return None

    ws.settimeout(0.0)
    _log(f"[DEBUG] Relay websocket established instance_id={relay_log_id}")
    return WebSocketChannelAdapter(ws)


def run_agent_with_relay(
    agent: str, args, unknown_args: Optional[Iterable[str]], api_key: str
) -> int:
    """Launch the agent CLI and mirror its terminal through the relay."""

    settings = RelayClientSettings()
    command = build_agent_command(agent, args, unknown_args, api_key)

    provided_instance_id = getattr(args, "agent_instance_id", None)
    agent_instance_id: Optional[str] = provided_instance_id or os.environ.get(
        "OMNARA_AGENT_INSTANCE_ID"
    )
    api_client: Optional[OmnaraClient] = None

    relay_username: Optional[str] = None

    base_url = getattr(args, "base_url", None) or os.environ.get("OMNARA_API_URL")
    base_url = base_url or "https://agent.omnara.com"

    if settings.enabled:
        temp_client: Optional[OmnaraClient] = None
        try:
            temp_client = OmnaraClient(
                api_key=api_key,
                base_url=base_url,
                log_func=_log,
            )
            registration = temp_client.register_agent_instance(
                agent_type=agent,
                transport="ws",
                agent_instance_id=agent_instance_id,
                name=getattr(args, "name", None),
            )
            agent_instance_id = registration.agent_instance_id
            relay_username = agent_instance_id
            api_client = temp_client
            temp_client = None
            _log(
                f"[DEBUG] Registered agent instance {agent_instance_id} for relay streaming"
            )
            if agent_instance_id:
                try:
                    api_client.send_message(
                        content="Started Terminal Session",
                        agent_instance_id=agent_instance_id,
                        agent_type=agent,
                    )
                    _log(
                        f"[DEBUG] Posted session start marker for instance {agent_instance_id}"
                    )
                except Exception as exc:  # pragma: no cover - defensive path
                    _log(
                        "[WARN] Unable to post session start marker: "
                        f"{exc!r} (instance_id={agent_instance_id})"
                    )
        except (AuthenticationError, APIError, TimeoutError) as exc:
            _log(
                f"[WARN] Failed to register agent instance {agent_instance_id or ''}: {exc}"
            )
        except Exception as exc:  # pragma: no cover - defensive path
            _log(
                f"[WARN] Unexpected error registering agent instance {agent_instance_id or ''}: {exc!r}"
            )
        finally:
            if temp_client is not None:
                temp_client.close()

    if relay_username is None:
        relay_username = agent_instance_id

    relay_log_id = relay_username or "offline"

    channel = None
    if settings.enabled:
        session_id = relay_username or agent_instance_id or "default"
        channel = _connect_websocket_channel(
            settings, session_id, api_key, relay_log_id
        )

    last_window: Optional[tuple[int, int]] = None  # (cols, rows)

    child_env = os.environ.copy()
    child_env.setdefault("OMNARA_API_KEY", api_key)
    if getattr(args, "base_url", None):
        child_env.setdefault("OMNARA_API_URL", args.base_url)
    if agent_instance_id:
        child_env["OMNARA_AGENT_INSTANCE_ID"] = agent_instance_id
        child_env.setdefault("OMNARA_SESSION_ID", agent_instance_id)

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

    def _notify_relay_of_resize(cols: int, rows: int) -> None:
        if channel is None:
            return

        payload = RESIZE_PAYLOAD.pack(rows, cols)
        frame = _pack_frame(FRAME_TYPE_RESIZE, payload)
        try:
            channel.sendall(frame)
            _log(
                f"[TRACE] sent resize cols={cols} rows={rows} (instance_id={relay_log_id})"
            )
        except Exception as exc:
            _log(
                f"[WARN] Relay resize send failed instance_id={relay_log_id} exc={exc!r}"
            )

    def _set_master_window(cols: int, rows: int, *, notify_relay: bool) -> None:
        nonlocal last_window

        cols = max(1, int(cols))
        rows = max(1, int(rows))

        if last_window == (cols, rows):
            if notify_relay:
                _notify_relay_of_resize(cols, rows)
            return

        winsize = struct.pack("HHHH", rows, cols, 0, 0)
        try:
            fcntl.ioctl(master_fd, termios.TIOCSWINSZ, winsize)
        except Exception:
            pass

        last_window = (cols, rows)

        if notify_relay:
            _notify_relay_of_resize(cols, rows)

    def _sync_local_terminal_size() -> None:
        try:
            cols, rows = get_terminal_size(fallback=(80, 24))
        except OSError:
            cols, rows = (80, 24)

        _set_master_window(cols, rows, notify_relay=True)

    if sys.stdin and sys.stdin.isatty():
        stdin_fd = sys.stdin.fileno()
        try:
            tty_state = termios.tcgetattr(stdin_fd)
            tty.setraw(stdin_fd)
        except Exception:
            tty_state = None

        _sync_local_terminal_size()

        def _on_sigwinch(signum, frame):  # type: ignore[override]
            _sync_local_terminal_size()

        try:
            sigwinch_prev_handler = signal.signal(signal.SIGWINCH, _on_sigwinch)
        except Exception:
            sigwinch_prev_handler = None
    else:
        _sync_local_terminal_size()

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
    last_reconnect_attempt = 0.0
    reconnect_interval = 5.0  # Try reconnecting every 5 seconds

    def _send_to_channel(data: bytes) -> None:
        if channel is None:
            return

        frame = _pack_frame(FRAME_TYPE_OUTPUT, data)
        try:
            channel.sendall(frame)
            _log(
                f"[TRACE] upstream chunk len={len(data)} sample={data[:32]!r} (instance_id={relay_log_id})"
            )
        except Exception as exc:
            _log(f"[WARN] Relay send failed instance_id={relay_log_id} exc={exc!r}")

    try:
        while True:
            # Attempt reconnection if channel is disconnected
            if channel is None and settings.enabled:
                import time

                current_time = time.time()
                if current_time - last_reconnect_attempt >= reconnect_interval:
                    last_reconnect_attempt = current_time
                    _log(
                        f"[INFO] Attempting to reconnect to relay (instance_id={relay_log_id})"
                    )
                    try:
                        # Reconstruct session_id from relay_username
                        reconnect_session_id = (
                            relay_username or agent_instance_id or "default"
                        )
                        new_channel = _connect_websocket_channel(
                            settings, reconnect_session_id, api_key, relay_log_id
                        )
                        if new_channel is not None:
                            channel = new_channel
                            channel_fd = channel.fileno()
                            channel_buffer.clear()
                            _log(
                                f"[INFO] Successfully reconnected to relay (instance_id={relay_log_id})"
                            )
                    except Exception as exc:
                        _log(
                            f"[WARN] Reconnection failed instance_id={relay_log_id}: {exc!r}"
                        )

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

            if channel is not None and channel_fd is not None and channel_fd in ready:
                try:
                    data = channel.recv(8192)
                except socket.timeout:
                    data = b""
                except Exception as exc:
                    _log(
                        f"[WARN] Relay receive failed instance_id={relay_log_id} exc={exc!r}"
                    )
                    if channel is not None:
                        try:
                            channel.close()
                        except Exception:
                            pass
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
                        f"[TRACE] raw downstream len={len(chunk_bytes)} sample={chunk_bytes[:32]!r} (instance_id={relay_log_id})"
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

                        payload = bytes(channel_buffer[FRAME_HEADER.size : total_len])
                        del channel_buffer[:total_len]

                        if frame_type == FRAME_TYPE_INPUT:
                            _log(
                                f"[TRACE] decoded downstream len={len(payload)} sample={payload[:32]!r} (instance_id={relay_log_id})"
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
                        elif frame_type == FRAME_TYPE_RESIZE:
                            if len(payload) != RESIZE_PAYLOAD.size:
                                _log(
                                    f"[WARN] Ignoring invalid resize payload len={len(payload)} (instance_id={relay_log_id})"
                                )
                                continue

                            rows, cols = RESIZE_PAYLOAD.unpack(payload)
                            _log(
                                f"[TRACE] applying remote resize cols={cols} rows={rows} (instance_id={relay_log_id})"
                            )
                            _set_master_window(cols, rows, notify_relay=False)
                        else:
                            _log(
                                f"[WARN] Ignoring frame type {frame_type} len={frame_len} (instance_id={relay_log_id})"
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
                exit_code = os.WEXITSTATUS(status) if os.WIFEXITED(status) else 1
                _log(
                    f"[INFO] Agent process exited with code {exit_code} (instance_id={relay_log_id})"
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
        final_exit_code = 0
    elif os.WIFEXITED(exit_status):
        final_exit_code = os.WEXITSTATUS(exit_status)
    else:
        final_exit_code = 1

    if api_client is not None:
        try:
            if agent_instance_id:
                # End the session in the Omnara dashboard
                api_client.end_session(agent_instance_id)
                _log(f"[INFO] Ended session in Omnara dashboard id={agent_instance_id}")
        except Exception as exc:  # pragma: no cover - best effort logging
            _log(
                f"[WARN] Failed to end session in Omnara dashboard id={agent_instance_id} exc={exc!r}"
            )
        finally:
            api_client.close()

    return final_exit_code
