"""In-memory session management for the omnara terminal relay."""

from __future__ import annotations

import asyncio
import struct
import time
import weakref
from collections import deque
from dataclasses import dataclass, field
from datetime import datetime
from pathlib import Path
from typing import Any, Deque, Dict, Iterable, Literal, Optional, Tuple

from .protocol import (
    FRAME_TYPE_INPUT,
    FRAME_TYPE_OUTPUT,
    FRAME_TYPE_RESIZE,
    FRAME_TYPE_SWITCH_TO_TMUX,
    FRAME_TYPE_SWITCH_TO_AGENT,
    FRAME_TYPE_MODE_CHANGED,
    pack_frame,
)

RESIZE_PAYLOAD = struct.Struct("!HH")

# Setup logging for handoff debugging
HANDOFF_LOG_FILE = Path.home() / ".omnara" / "logs" / "terminal_handoff.log"

def _log_handoff(message: str) -> None:
    """Log handoff events to a dedicated file for debugging."""
    try:
        HANDOFF_LOG_FILE.parent.mkdir(parents=True, exist_ok=True)
        timestamp = datetime.utcnow().isoformat()
        with HANDOFF_LOG_FILE.open("a", encoding="utf-8") as f:
            f.write(f"[{timestamp}] [SESSION] {message}\n")
            f.flush()
    except Exception:
        pass


@dataclass(slots=True)
class Session:
    """Represents a single CLI session flowing through the relay."""

    user_id: str
    session_id: str
    history_limit: int
    api_key_hash: str
    started_at: float = field(default_factory=lambda: time.time())
    last_heartbeat: float = field(default_factory=lambda: time.time())
    is_active: bool = True
    ended_at: Optional[float] = None
    metadata: Dict[str, str] = field(default_factory=dict)
    cols: int = 80
    rows: int = 24
    mode: Literal["agent", "tmux", "transitioning"] = "agent"

    _history: Deque[bytes] = field(default_factory=deque, init=False)
    _history_size: int = 0
    _websockets: "weakref.WeakSet[Any]" = field(
        default_factory=weakref.WeakSet, init=False
    )
    _agent_socket: Optional[Any] = None

    def attach_agent_socket(self, ws: Any) -> None:
        """Associate an agent WebSocket connection as the upstream."""

        self._agent_socket = ws

    def detach_agent_socket(self) -> None:
        """Detach the agent WebSocket connection."""

        self._agent_socket = None

    def register_websocket(self, ws: Any) -> None:
        """Track a websocket client to broadcast live output."""

        self._websockets.add(ws)

    def unregister_websocket(self, ws: Any) -> None:
        """Remove a websocket client from the broadcast set."""

        self._websockets.discard(ws)

    def append_output(self, chunk: bytes) -> None:
        """Store terminal output in a bounded ring buffer and broadcast."""

        if not chunk:
            return

        self._history.append(chunk)
        self._history_size += len(chunk)
        self.heartbeat()

        print(
            f"[relay] append_output len={len(chunk)} session={self.user_id}:{self.session_id} history={self._history_size}",
            flush=True,
        )

        while self._history_size > self.history_limit and self._history:
            dropped = self._history.popleft()
            self._history_size -= len(dropped)

        # Broadcast asynchronously so we never block the upstream stream
        frame = pack_frame(FRAME_TYPE_OUTPUT, chunk)
        for ws in list(self._websockets):
            asyncio.create_task(self._send_bytes(ws, frame))

    async def _send_bytes(self, ws: Any, frame: bytes) -> None:
        try:
            await ws.send_bytes(frame)
            print(
                f"[relay] send_bytes len={len(frame)} session={self.user_id}:{self.session_id}",
                flush=True,
            )
        except Exception as exc:
            print(
                f"[relay] send_bytes FAILED session={self.user_id}:{self.session_id} error={exc!r}",
                flush=True,
            )
            self._websockets.discard(ws)

    def forward_input(self, data: str) -> None:
        """Ship input from clients back to the CLI."""

        if not data or self._agent_socket is None:
            return

        payload = data.encode()
        frame = pack_frame(FRAME_TYPE_INPUT, payload)
        print(
            f"[relay] forward_input len={len(payload)} session={self.user_id}:{self.session_id}",
            flush=True,
        )
        asyncio.create_task(self._send_to_agent(frame))

    def request_resize(
        self, cols: int | float | None, rows: int | float | None
    ) -> None:
        """Request a PTY resize originating from a viewer.

        If rows is not provided, keeps current height and only resizes width.
        """

        if self._agent_socket is None:
            return

        try:
            int_cols = int(cols) if cols is not None else None
            int_rows = int(rows) if rows is not None else None
        except (TypeError, ValueError):
            return

        # Use current dimensions if not provided
        if int_cols is None:
            int_cols = self.cols
        if int_rows is None:
            int_rows = self.rows

        # Need at least one valid dimension
        if int_cols is None or int_rows is None:
            return

        if int_cols <= 0 or int_rows <= 0:
            return

        # No change needed
        if self.cols == int_cols and self.rows == int_rows:
            return

        payload = RESIZE_PAYLOAD.pack(int_rows, int_cols)
        frame = pack_frame(FRAME_TYPE_RESIZE, payload)
        print(
            f"[relay] request_resize cols={int_cols} rows={int_rows} session={self.user_id}:{self.session_id}",
            flush=True,
        )
        asyncio.create_task(self._send_to_agent(frame))

        self.update_size(int_cols, int_rows)

    def update_size(self, cols: int, rows: int) -> None:
        """Track the PTY window size and broadcast to viewers."""

        if cols <= 0 or rows <= 0:
            return

        changed = self.cols != cols or self.rows != rows
        self.cols = cols
        self.rows = rows

        if not changed:
            return

        payload = {
            "type": "resize",
            "session_id": self.session_id,
            "cols": cols,
            "rows": rows,
        }

        for ws in list(self._websockets):
            asyncio.create_task(self._send_json(ws, payload))

    def iter_history(self) -> Iterable[bytes]:
        """Iterate over buffered history chunks."""

        return tuple(self._history)

    def heartbeat(self) -> None:
        """Update the heartbeat timestamp for leak detection."""

        self.last_heartbeat = time.time()

    def end(self) -> None:
        """Mark the session inactive and notify listeners."""

        if not self.is_active:
            return

        self.is_active = False
        self.ended_at = time.time()
        self.detach_agent_socket()

        for ws in list(self._websockets):
            asyncio.create_task(
                self._send_json(
                    ws, {"type": "session_ended", "session_id": self.session_id}
                )
            )

    async def _send_json(self, ws: Any, payload: dict) -> None:
        try:
            await ws.send_json(payload)
        except Exception as exc:
            print(
                f"[relay] send_json FAILED session={self.user_id}:{self.session_id} error={exc!r}",
                flush=True,
            )
            self._websockets.discard(ws)

    async def _send_to_agent(self, frame: bytes) -> None:
        if self._agent_socket is None:
            return

        try:
            await self._agent_socket.send_bytes(frame)
        except Exception as exc:
            print(
                f"[relay] send_to_agent FAILED session={self.user_id}:{self.session_id} error={exc!r}",
                flush=True,
            )
            self._agent_socket = None

    def request_switch_to_tmux(self) -> None:
        """Request agent to switch from agent mode to tmux mode."""
        _log_handoff(f"request_switch_to_tmux called for session {self.session_id}, agent_socket={'connected' if self._agent_socket else 'None'}")
        if self._agent_socket is None:
            _log_handoff(f"Cannot switch to tmux: no agent socket for session {self.session_id}")
            return

        self.mode = "transitioning"
        frame = pack_frame(FRAME_TYPE_SWITCH_TO_TMUX, b"")
        _log_handoff(f"Sending SWITCH_TO_TMUX frame to agent for session {self.session_id}")
        print(
            f"[relay] request_switch_to_tmux session={self.user_id}:{self.session_id}",
            flush=True,
        )
        asyncio.create_task(self._send_to_agent(frame))

    def request_switch_to_agent(self) -> None:
        """Request agent to switch from tmux mode to agent mode."""
        _log_handoff(f"request_switch_to_agent called for session {self.session_id}")
        if self._agent_socket is None:
            _log_handoff(f"Cannot switch to agent: no agent socket for session {self.session_id}")
            return

        self.mode = "transitioning"
        frame = pack_frame(FRAME_TYPE_SWITCH_TO_AGENT, b"")
        _log_handoff(f"Sending SWITCH_TO_AGENT frame to agent for session {self.session_id}")
        print(
            f"[relay] request_switch_to_agent session={self.user_id}:{self.session_id}",
            flush=True,
        )
        asyncio.create_task(self._send_to_agent(frame))

    def broadcast_mode_change(
        self, mode: Literal["agent", "tmux"], cols: int, rows: int
    ) -> None:
        """Broadcast mode change to all connected websockets."""
        self.mode = mode
        self.cols = cols
        self.rows = rows

        payload = {
            "type": "mode_changed",
            "session_id": self.session_id,
            "mode": mode,
            "cols": cols,
            "rows": rows,
        }

        _log_handoff(f"Broadcasting mode change to {mode} ({cols}x{rows}) to {len(self._websockets)} viewers for session {self.session_id}")
        print(
            f"[relay] broadcast_mode_change mode={mode} cols={cols} rows={rows} session={self.user_id}:{self.session_id}",
            flush=True,
        )

        for ws in list(self._websockets):
            asyncio.create_task(self._send_json(ws, payload))


class SessionManager:
    """Thread-safe registry of active and recently-ended sessions."""

    def __init__(
        self,
        history_limit: int,
        heartbeat_miss_limit: int,
        heartbeat_interval: int,
        ended_retention_seconds: int,
    ) -> None:
        self._history_limit = history_limit
        self._heartbeat_miss_limit = heartbeat_miss_limit
        self._heartbeat_interval = heartbeat_interval
        self._ended_retention_seconds = ended_retention_seconds
        self._sessions: Dict[Tuple[str, str], Session] = {}
        self._lock = asyncio.Lock()

    async def create_session(
        self, user_id: str, session_id: str, api_key_hash: str
    ) -> Session:
        """Create and register a new session."""

        async with self._lock:
            key = (user_id, session_id)
            session = self._sessions.get(key)
            if session is None:
                session = Session(
                    user_id=user_id,
                    session_id=session_id,
                    history_limit=self._history_limit,
                    api_key_hash=api_key_hash,
                )
                self._sessions[key] = session
            else:
                session.api_key_hash = api_key_hash
                session.is_active = True
                session.ended_at = None
                session.detach_agent_socket()
            return session

    async def get_session(
        self, user_id: str, session_id: str, api_key_hash: str | None
    ) -> Optional[Session]:
        async with self._lock:
            session = self._sessions.get((user_id, session_id))
            if session and (
                api_key_hash is None or session.api_key_hash == api_key_hash
            ):
                return session
            return None

    async def sessions_for_user(
        self, user_id: str, api_key_hash: str | None
    ) -> Tuple[Session, ...]:
        async with self._lock:
            return tuple(
                session
                for (uid, _), session in self._sessions.items()
                if uid == user_id
                and (api_key_hash is None or session.api_key_hash == api_key_hash)
            )

    async def end_session(self, user_id: str, session_id: str) -> None:
        async with self._lock:
            key = (user_id, session_id)
            session = self._sessions.get(key)
            if session:
                session.end()

    async def drop_session(self, user_id: str, session_id: str) -> None:
        async with self._lock:
            self._sessions.pop((user_id, session_id), None)

    async def reap_inactive(self) -> Tuple[Session, ...]:
        """Remove sessions whose heartbeat has expired or which aged out."""

        now = time.time()
        expired: Tuple[Session, ...] = ()
        async with self._lock:
            to_remove = []
            for key, session in self._sessions.items():
                if not session.is_active:
                    if (
                        session.ended_at is not None
                        and now - session.ended_at > self._ended_retention_seconds
                    ):
                        to_remove.append(key)

            if to_remove:
                expired_list = []
                for key in to_remove:
                    expired_list.append(self._sessions.pop(key))
                expired = tuple(expired_list)

        return expired
