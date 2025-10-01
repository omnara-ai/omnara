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
import subprocess
import sys
import tempfile
import termios
import threading
import time
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
HANDOFF_LOG_FILE = Path.home() / ".omnara" / "logs" / "terminal_handoff.log"

FRAME_HEADER = struct.Struct("!BI")
FRAME_TYPE_OUTPUT = 0
FRAME_TYPE_INPUT = 1
FRAME_TYPE_RESIZE = 2
FRAME_TYPE_SWITCH_TO_TMUX = 3
FRAME_TYPE_SWITCH_TO_AGENT = 4
FRAME_TYPE_MODE_CHANGED = 5
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


def _pack_mode_changed_frame(mode: str, cols: int, rows: int) -> bytes:
    """Pack a MODE_CHANGED frame with mode and dimensions."""
    mode_byte = 1 if mode == "tmux" else 0
    payload = bytes([mode_byte]) + cols.to_bytes(4, "big") + rows.to_bytes(4, "big")
    return _pack_frame(FRAME_TYPE_MODE_CHANGED, payload)


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


def _log_handoff(message: str) -> None:
    """Log handoff events to a dedicated file for debugging."""
    try:
        HANDOFF_LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        timestamp = datetime.utcnow().isoformat()
        with HANDOFF_LOG_FILE.open("a", encoding="utf-8") as f:
            f.write(f"[{timestamp}] [CLIENT] {message}\n")
            f.flush()
    except Exception as e:
        print(f"[CLIENT] Failed to log: {e}", flush=True)


def _show_waiting_screen() -> None:
    """Display waiting screen when session is being viewed remotely."""
    message = """
╔════════════════════════════════════════════════════╗
║                                                    ║
║     Session is being viewed remotely               ║
║                                                    ║
║     Press any key to take control and resume agent ║
║                                                    ║
╚════════════════════════════════════════════════════╝
"""
    sys.stdout.write(message)
    sys.stdout.flush()


def _clear_waiting_screen() -> None:
    """Clear the waiting screen."""
    # Clear screen
    sys.stdout.write("\033[2J\033[H")
    sys.stdout.flush()


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

    _log_handoff(f"[ENTRY] run_agent_with_relay called for agent={agent}")

    settings = RelayClientSettings()

    # Check if tmux is installed (required for terminal handoff feature)
    if settings.enabled and which("tmux") is None:
        print("\n❌ Error: tmux is not installed", file=sys.stderr)
        print("The terminal handoff feature requires tmux to be installed.", file=sys.stderr)
        print("\nTo install tmux:", file=sys.stderr)
        print("  macOS:   brew install tmux", file=sys.stderr)
        print("  Linux:   apt-get install tmux  or  yum install tmux", file=sys.stderr)
        print("\nAlternatively, run without relay: omnara --no-relay\n", file=sys.stderr)
        return 1

    provided_instance_id = getattr(args, "agent_instance_id", None)
    agent_instance_id: Optional[str] = provided_instance_id or os.environ.get(
        "OMNARA_AGENT_INSTANCE_ID"
    )

    # If no instance ID provided, generate one for this session
    if agent_instance_id is None:
        import uuid
        agent_instance_id = str(uuid.uuid4())
        _log_handoff(f"[CONFIG] Generated new agent instance ID: {agent_instance_id}")

    # Build command with resume/session-id so we can continue the same session
    command = build_agent_command(agent, args, unknown_args, api_key)

    # Add session ID to command so the initial run uses it
    if agent == "codex":
        command = ["codex", "resume", agent_instance_id]
    elif agent == "claude":
        command = ["claude", "--session-id", agent_instance_id]

    _log_handoff(f"[CONFIG] Command: {' '.join(command)}, relay enabled: {settings.enabled}")
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

    # Mode tracking for agent/tmux handoff
    current_mode = "agent"
    should_stream_to_relay = False
    tmux_session_name = agent_instance_id or "omnara-session"
    pending_mode_switch: Optional[str] = None  # "tmux" or "agent"
    stdin_monitoring_disabled_until = 0.0  # Timestamp to ignore stdin until
    tmux_process = None  # subprocess.Popen for tmux
    tmux_thread = None  # threading.Thread for streaming

    _log_handoff(f"[INIT] Handoff system initialized: mode={current_mode}, stream={should_stream_to_relay}, session={tmux_session_name}")

    def _send_to_channel(data: bytes) -> None:
        nonlocal should_stream_to_relay
        if channel is None or not should_stream_to_relay:
            return

        frame = _pack_frame(FRAME_TYPE_OUTPUT, data)
        try:
            channel.sendall(frame)
            _log(
                f"[TRACE] upstream chunk len={len(data)} sample={data[:32]!r} (instance_id={relay_log_id})"
            )
        except Exception as exc:
            _log(f"[WARN] Relay send failed instance_id={relay_log_id} exc={exc!r}")

    def _switch_to_tmux() -> bool:
        """Switch from agent to tmux mode. Returns True if successful."""
        nonlocal child_pid, current_mode, should_stream_to_relay, last_window, master_fd, stdin_monitoring_disabled_until
        nonlocal tmux_process, tmux_thread

        _log(f"[INFO] Switching to tmux mode (instance_id={relay_log_id})")
        _log_handoff(f"[ENTRY] _switch_to_tmux called")

        # Kill the agent process
        if child_pid:
            try:
                os.kill(child_pid, signal.SIGTERM)
                _log(f"[INFO] Sent SIGTERM to agent pid={child_pid}")
                _log_handoff(f"[INFO] Sent SIGTERM to agent pid={child_pid}")

                # Wait up to 5 seconds for graceful shutdown
                for _ in range(50):
                    try:
                        finished_pid, _ = os.waitpid(child_pid, os.WNOHANG)
                        if finished_pid == child_pid:
                            _log_handoff(f"[INFO] Agent process {child_pid} exited gracefully")
                            break
                    except ChildProcessError:
                        _log_handoff(f"[INFO] Agent process {child_pid} already exited")
                        break
                    time.sleep(0.1)
                else:
                    # Force kill if still running
                    try:
                        os.kill(child_pid, signal.SIGKILL)
                        os.waitpid(child_pid, 0)
                        _log(f"[INFO] Force killed agent pid={child_pid}")
                        _log_handoff(f"[INFO] Force killed agent pid={child_pid}")
                    except Exception:
                        pass
            except Exception as exc:
                _log(f"[WARN] Failed to kill agent: {exc!r}")
                _log_handoff(f"[WARN] Failed to kill agent: {exc!r}")

        # Close old master_fd
        try:
            os.close(master_fd)
            _log_handoff(f"[INFO] Closed master_fd")
        except Exception as exc:
            _log_handoff(f"[WARN] Failed to close master_fd: {exc!r}")
            pass

        # Check if tmux is available
        if which("tmux") is None:
            _log("[ERROR] tmux not found, cannot switch to tmux mode")
            _log_handoff("[ERROR] tmux not found, cannot switch to tmux mode")
            return False

        # Get current terminal size (from phone/web)
        cols, rows = last_window if last_window else (80, 24)
        _log_handoff(f"[INFO] Using dimensions: {cols}x{rows}")

        # Kill any existing tmux session with this name
        try:
            subprocess.run(
                ["tmux", "kill-session", "-t", tmux_session_name],
                check=False,
                capture_output=True,
            )
            _log_handoff(f"[INFO] Killed any existing tmux session {tmux_session_name}")
        except Exception as exc:
            _log_handoff(f"[WARN] Error killing existing tmux session: {exc!r}")

        # Start tmux session in detached mode, running the agent command inside it
        try:
            # Build the tmux command to start the agent with proper environment
            tmux_cmd = ["tmux", "new-session", "-d", "-s", tmux_session_name, "-x", str(cols), "-y", str(rows)]

            # Add environment variables
            for key, value in child_env.items():
                tmux_cmd.extend(["-e", f"{key}={value}"])

            # Add the agent command at the end
            tmux_cmd.extend(command)

            _log_handoff(f"[INFO] Starting tmux with command: {' '.join(command)}")
            result = subprocess.run(
                tmux_cmd,
                check=True,
                capture_output=True,
            )
            _log_handoff(f"[INFO] Created detached tmux session {tmux_session_name} ({cols}x{rows}) running {command[0]}")
            subprocess.run(
                ["tmux", "set", "-t", tmux_session_name, "mouse", "on"],
                check=True,
                capture_output=True,
            )
            _log(f"[INFO] Started tmux session {tmux_session_name} ({cols}x{rows})")
            _log_handoff(f"[SUCCESS] Started tmux session {tmux_session_name} ({cols}x{rows}) with agent")
        except subprocess.CalledProcessError as exc:
            _log(f"[ERROR] Failed to start tmux: {exc!r}")
            _log_handoff(f"[ERROR] Failed to start tmux: {exc}, stdout={exc.stdout}, stderr={exc.stderr}")
            return False
        except Exception as exc:
            _log(f"[ERROR] Unexpected error starting tmux: {exc!r}")
            _log_handoff(f"[ERROR] Unexpected error starting tmux: {exc!r}")
            return False

        # Use tmux pipe-pane to capture output to a file, then stream that file
        # This is more reliable than trying to attach tmux

        # Create a temporary file for tmux output
        tmux_output_file = tempfile.NamedTemporaryFile(mode='w+b', delete=False, prefix='omnara-tmux-')
        tmux_output_path = tmux_output_file.name
        tmux_output_file.close()
        _log_handoff(f"[INFO] Created tmux output file: {tmux_output_path}")

        # Enable tmux pipe-pane to capture output
        try:
            subprocess.run(
                ["tmux", "pipe-pane", "-t", tmux_session_name, "-o", f"cat >> {tmux_output_path}"],
                check=True,
                capture_output=True,
            )
            _log_handoff(f"[INFO] Enabled tmux pipe-pane to {tmux_output_path}")
        except Exception as exc:
            _log_handoff(f"[ERROR] Failed to enable pipe-pane: {exc!r}")
            return False

        # We don't need to attach tmux - just monitor the session
        # Store a reference to check if session is alive
        child_pid = -1  # No child process in tmux mode
        master_fd = -1  # Not using PTY anymore
        _log_handoff(f"[INFO] tmux session running in detached mode, no attach needed")

        # Set mode and streaming flag BEFORE starting the thread
        current_mode = "tmux"
        should_stream_to_relay = True
        _log_handoff(f"[INFO] Set mode to tmux, streaming enabled")

        # Background thread to stream tmux output file to relay
        def stream_tmux_to_relay():
            """Background thread to tail tmux output and stream to relay."""
            _log_handoff(f"[THREAD] Started tmux streaming thread")
            # Wait a tiny bit for the file to have initial content
            time.sleep(0.1)
            try:
                with open(tmux_output_path, 'rb') as f:
                    _log_handoff(f"[THREAD] Opened output file, starting to stream")
                    while current_mode == "tmux":
                        # Check if tmux session still exists
                        try:
                            result = subprocess.run(
                                ["tmux", "has-session", "-t", tmux_session_name],
                                capture_output=True,
                                timeout=1
                            )
                            session_exists = result.returncode == 0
                            if not session_exists:
                                _log_handoff(f"[THREAD] tmux session no longer exists, exiting")
                                break
                        except Exception as exc:
                            _log_handoff(f"[THREAD] Error checking tmux session: {exc!r}")
                            break

                        data = f.read(8192)
                        if data and channel is not None and should_stream_to_relay:
                            frame = _pack_frame(FRAME_TYPE_OUTPUT, data)
                            try:
                                channel.sendall(frame)
                                _log_handoff(f"[THREAD] Sent {len(data)} bytes to relay")
                            except Exception as exc:
                                _log_handoff(f"[THREAD ERROR] Failed to send tmux output to relay: {exc!r}")
                                break
                        else:
                            # No data available, sleep briefly
                            time.sleep(0.05)
                _log_handoff(f"[THREAD] Exiting tmux streaming thread (mode={current_mode})")
            except Exception as exc:
                _log_handoff(f"[THREAD ERROR] tmux streaming thread error: {exc!r}")
            finally:
                # Clean up output file
                try:
                    os.unlink(tmux_output_path)
                    _log_handoff(f"[THREAD] Cleaned up tmux output file")
                except Exception as exc:
                    _log_handoff(f"[THREAD] Failed to cleanup output file: {exc!r}")

        # Start background thread to stream tmux output
        tmux_thread = threading.Thread(target=stream_tmux_to_relay, daemon=True)
        tmux_thread.start()
        _log_handoff(f"[INFO] Started tmux streaming thread")

        # Send mode changed notification to relay
        if channel is not None:
            try:
                frame = _pack_mode_changed_frame("tmux", cols, rows)
                channel.sendall(frame)
                _log(f"[INFO] Sent mode_changed frame: tmux {cols}x{rows}")
            except Exception as exc:
                _log(f"[WARN] Failed to send mode_changed frame: {exc!r}")

        # Disable stdin monitoring for 1 second to avoid spurious switch-back
        stdin_monitoring_disabled_until = time.time() + 1.0

        # Clear screen and show waiting screen on local terminal
        os.write(sys.stdout.fileno(), b"\033[2J\033[H")
        _show_waiting_screen()

        return True

    def _switch_to_agent() -> bool:
        """Switch from tmux to agent mode. Returns True if successful."""
        nonlocal child_pid, current_mode, should_stream_to_relay, last_window, master_fd
        nonlocal tmux_process, tmux_thread

        _log(f"[INFO] Switching to agent mode (instance_id={relay_log_id})")
        _log_handoff(f"[ENTRY] _switch_to_agent called")

        # Stop streaming before killing tmux
        should_stream_to_relay = False
        current_mode = "transitioning"
        _log_handoff(f"[INFO] Set mode to transitioning, stopped streaming")

        # Wait for streaming thread to finish (it will exit when current_mode != "tmux")
        if tmux_thread and tmux_thread.is_alive():
            _log_handoff(f"[INFO] Waiting for streaming thread to finish...")
            tmux_thread.join(timeout=3)
            if tmux_thread.is_alive():
                _log_handoff(f"[WARN] Streaming thread still alive after 3s, continuing anyway")
            else:
                _log_handoff(f"[INFO] Streaming thread finished")
        tmux_thread = None
        tmux_process = None  # We don't have a process to kill

        # Kill tmux session
        try:
            subprocess.run(
                ["tmux", "kill-session", "-t", tmux_session_name],
                check=False,
                capture_output=True,
            )
            _log(f"[INFO] Killed tmux session {tmux_session_name}")
            _log_handoff(f"[INFO] Killed tmux session {tmux_session_name}")
        except Exception as exc:
            _log(f"[WARN] Failed to kill tmux session: {exc!r}")
            _log_handoff(f"[WARN] Failed to kill tmux session: {exc!r}")

        # Clear waiting screen
        _clear_waiting_screen()

        # Resume agent with session ID
        agent_cmd = command[:]
        if agent == "codex":
            agent_cmd = ["codex", "resume", tmux_session_name]
        elif agent == "claude":
            agent_cmd = ["claude", "--session-id", tmux_session_name]

        _log(f"[INFO] Resuming agent with command: {' '.join(agent_cmd)}")
        _log_handoff(f"[INFO] Resuming agent, it will auto-detect local terminal size")

        # Fork new agent process - it will auto-detect terminal size from PTY
        new_pid, new_master = pty.fork()
        if new_pid == 0:
            # Child: exec agent
            os.execvpe(agent_cmd[0], agent_cmd, child_env)

        child_pid = new_pid
        master_fd = new_master

        # Set master_fd to non-blocking
        try:
            flags = fcntl.fcntl(master_fd, fcntl.F_GETFL)
            fcntl.fcntl(master_fd, fcntl.F_SETFL, flags | os.O_NONBLOCK)
        except Exception:
            pass

        # PTY automatically inherits terminal size - don't set it manually!
        _log_handoff(f"[INFO] PTY will auto-detect terminal size from parent")

        # Get terminal size just for the relay notification
        try:
            local_size = get_terminal_size()
            cols, rows = local_size.columns, local_size.lines
        except Exception:
            cols, rows = 80, 24

        # Send mode changed notification to relay
        if channel is not None:
            try:
                frame = _pack_mode_changed_frame("agent", cols, rows)
                channel.sendall(frame)
                _log(f"[INFO] Sent mode_changed frame: agent {cols}x{rows}")
            except Exception as exc:
                _log(f"[WARN] Failed to send mode_changed frame: {exc!r}")

        current_mode = "agent"
        should_stream_to_relay = False

        return True

    try:
        while True:
            # Attempt reconnection if channel is disconnected
            if channel is None and settings.enabled:
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

            # In tmux mode, don't read from master_fd (it's set to -1)
            fds = []
            if current_mode == "agent" and master_fd >= 0:
                fds.append(master_fd)
            if channel_fd is not None:
                fds.append(channel_fd)
            if stdin_fd is not None:
                fds.append(stdin_fd)

            ready, _, _ = select.select(fds, [], [], 0.1)

            # Only read from master_fd in agent mode
            if current_mode == "agent" and master_fd >= 0 and master_fd in ready:
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
                            _log_handoff(f"[INPUT] Received input from relay: {len(payload)} bytes, mode={current_mode}")

                            # In tmux mode, forward input to tmux using send-keys -l (literal)
                            if current_mode == "tmux":
                                try:
                                    # Use tmux send-keys with -l flag for literal input
                                    # This sends the exact bytes to the tmux session
                                    text = payload.decode('utf-8', errors='replace')
                                    subprocess.run(
                                        ["tmux", "send-keys", "-t", tmux_session_name, "-l", text],
                                        check=True,
                                        capture_output=True,
                                        timeout=1
                                    )
                                    _log_handoff(f"[INPUT] Forwarded {len(payload)} bytes to tmux via send-keys")
                                except Exception as exc:
                                    _log_handoff(f"[INPUT ERROR] Failed to forward input to tmux: {exc!r}")
                            # In agent mode, forward to master_fd
                            elif current_mode == "agent" and master_fd >= 0:
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
                            _log_handoff(f"[RESIZE] Received resize: {cols}x{rows}, mode={current_mode}")

                            # Update last_window for future use
                            last_window = (cols, rows)

                            # In tmux mode, resize the tmux session
                            if current_mode == "tmux":
                                try:
                                    subprocess.run(
                                        ["tmux", "resize-window", "-t", tmux_session_name, "-x", str(cols), "-y", str(rows)],
                                        check=True,
                                        capture_output=True,
                                    )
                                    _log_handoff(f"[RESIZE] Resized tmux session to {cols}x{rows}")
                                except Exception as exc:
                                    _log_handoff(f"[RESIZE ERROR] Failed to resize tmux: {exc!r}")
                            # In agent mode, resize the PTY
                            elif current_mode == "agent":
                                _set_master_window(cols, rows, notify_relay=False)
                        elif frame_type == FRAME_TYPE_SWITCH_TO_TMUX:
                            _log(
                                f"[INFO] Received switch_to_tmux signal (instance_id={relay_log_id})"
                            )
                            _log_handoff(
                                f"Received SWITCH_TO_TMUX frame, current_mode={current_mode}"
                            )
                            pending_mode_switch = "tmux"
                        elif frame_type == FRAME_TYPE_SWITCH_TO_AGENT:
                            _log(
                                f"[INFO] Received switch_to_agent signal (instance_id={relay_log_id})"
                            )
                            _log_handoff(
                                f"Received SWITCH_TO_AGENT frame, current_mode={current_mode}"
                            )
                            pending_mode_switch = "agent"
                        else:
                            _log(
                                f"[WARN] Ignoring frame type {frame_type} len={frame_len} (instance_id={relay_log_id})"
                            )

            if stdin_fd is not None and stdin_fd in ready:
                data = os.read(stdin_fd, 8192)
                if not data:
                    stdin_fd = None
                else:
                    # Check if stdin monitoring is temporarily disabled
                    if time.time() < stdin_monitoring_disabled_until:
                        _log_handoff(f"[DEBUG] Ignoring stdin input during disabled period")
                        continue

                    # If in tmux mode and user types locally, switch back to agent
                    if current_mode == "tmux":
                        _log(
                            f"[INFO] Local input detected in tmux mode, switching to agent (instance_id={relay_log_id})"
                        )
                        _log_handoff(f"[INFO] Local stdin detected, requesting switch back to agent")
                        pending_mode_switch = "agent"
                    else:
                        # In agent mode, forward input normally
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

            # Execute pending mode switches
            if pending_mode_switch == "tmux" and current_mode == "agent":
                _log_handoff(f"Executing pending switch to tmux, current_mode={current_mode}")
                if _switch_to_tmux():
                    pending_mode_switch = None
                    _log_handoff("Successfully switched to tmux mode")
                else:
                    # Failed to switch, clear pending
                    pending_mode_switch = None
                    _log(f"[ERROR] Failed to switch to tmux mode (instance_id={relay_log_id})")
                    _log_handoff("Failed to switch to tmux mode")
            elif pending_mode_switch == "agent" and current_mode == "tmux":
                _log_handoff(f"Executing pending switch to agent, current_mode={current_mode}")
                if _switch_to_agent():
                    pending_mode_switch = None
                    _log_handoff("Successfully switched to agent mode")
                else:
                    # Failed to switch, clear pending
                    pending_mode_switch = None
                    _log(f"[ERROR] Failed to switch to agent mode (instance_id={relay_log_id})")
                    _log_handoff("Failed to switch to agent mode")

            # Only check child process status in agent mode
            if current_mode == "agent" and child_pid > 0:
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
            elif current_mode == "tmux":
                # In tmux mode, check if the session still exists
                try:
                    result = subprocess.run(
                        ["tmux", "has-session", "-t", tmux_session_name],
                        capture_output=True,
                        timeout=1
                    )
                    if result.returncode != 0:
                        _log(f"[INFO] tmux session ended (instance_id={relay_log_id})")
                        _log_handoff(f"[INFO] tmux session no longer exists, exiting main loop")
                        break
                except Exception as exc:
                    _log_handoff(f"[ERROR] Failed to check tmux session: {exc!r}")
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

        try:
            os.close(master_fd)
        except OSError:
            pass  # Already closed by mode switch

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
