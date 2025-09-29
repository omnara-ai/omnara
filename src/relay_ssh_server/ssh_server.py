"""SSH server implementation for omnara terminal sharing."""

from __future__ import annotations

import asyncio
import struct
from typing import List, Optional

import asyncssh

from .auth import RelayAuthError, decode_api_key, hash_api_key
from .sessions import Session, SessionManager
from .protocol import FRAME_TYPE_OUTPUT, FRAME_TYPE_RESIZE, iter_frames


class RelaySSHServer(asyncssh.SSHServer):
    """Accepts CLI connections and provisions sessions."""

    def __init__(self, manager: SessionManager):
        self._manager = manager
        self._user_id: Optional[str] = None
        self._api_key_hash: Optional[str] = None
        self._conn: Optional[asyncssh.SSHServerConnection] = None

    def connection_made(self, conn: asyncssh.SSHServerConnection) -> None:
        """Remember connection details so we can inspect username/password."""

        self._conn = conn
        super().connection_made(conn)

    def password_auth_supported(self) -> bool:
        return True

    def validate_password(self, username: str, password: str) -> bool:
        try:
            user_id = decode_api_key(password)
            api_key_hash = hash_api_key(password)
        except RelayAuthError as exc:
            print(
                f"[relay] validate_password failed username={username} reason={exc}",
                flush=True,
            )
            return False

        self._user_id = user_id
        self._api_key_hash = api_key_hash
        print(
            f"[relay] validate_password user={user_id} username={username} -> True",
            flush=True,
        )
        return True

    def session_requested(self):
        username = None
        try:
            username = self._conn.get_extra_info("username")
            session_id = self._parse_session_id(username or "")

            if not self._user_id or not self._api_key_hash:
                raise RuntimeError("Authentication state missing during session setup")

            print(
                f"[relay] session_requested user={self._user_id} session={session_id}",
                flush=True,
            )

            return RelaySSHSession(
                self._manager,
                user_id=self._user_id,
                session_id=session_id,
                api_key_hash=self._api_key_hash,
            )
        except Exception as exc:  # pragma: no cover - defensive logging
            print(
                f"[relay] session_requested failed username={username} exc={exc}",
                flush=True,
            )
            raise

    @staticmethod
    def _parse_session_id(raw_username: str) -> str:
        parts = raw_username.split(":", 1)
        if len(parts) == 2 and parts[1]:
            return parts[1]
        return parts[0] or "default"


class RelaySSHSession(asyncssh.SSHServerSession):
    """SSH channel used to shuttle PTY data between CLI and relay."""

    def __init__(
        self,
        manager: SessionManager,
        user_id: str,
        session_id: str,
        api_key_hash: str,
    ):
        self._manager = manager
        self._session: Optional[Session] = None
        self._chan: Optional[asyncssh.SSHServerChannel] = None
        self._user_id = user_id
        self._session_id = session_id
        self._api_key_hash = api_key_hash
        self._pending_chunks: List[bytes] = []
        self._rx_buffer = bytearray()
        self._pending_resize: Optional[tuple[int, int]] = None

    def pty_requested(
        self,
        term: str,
        width: int,
        height: int,
        pixelwidth: int | None = None,
        pixelheight: int | None = None,
        modes: object | None = None,
    ) -> bool:
        print(
            f"[relay] pty_requested user={self._user_id} session={self._session_id} term={term}",
            flush=True,
        )
        return True

    def auth_success(self) -> None:
        print(
            f"[relay] auth_success user={self._user_id} session={self._session_id}",
            flush=True,
        )

    def shell_requested(self) -> bool:
        print(
            f"[relay] shell_requested user={self._user_id} session={self._session_id}",
            flush=True,
        )
        asyncio.create_task(self._register_session())
        return True

    async def _register_session(self) -> None:
        try:
            if self._api_key_hash is None:
                raise ValueError("API key hash missing for session registration")
            session = await self._manager.create_session(
                user_id=self._user_id,
                session_id=self._session_id,
                api_key_hash=self._api_key_hash,
            )
            self._session = session
            if self._chan:
                session.attach_channel(self._chan)

            if self._pending_chunks:
                for chunk in self._pending_chunks:
                    session.append_output(chunk)
                self._pending_chunks.clear()

            if self._pending_resize:
                cols, rows = self._pending_resize
                session.update_size(cols, rows)
                self._pending_resize = None
            print(
                f"[relay] session ready user={self._user_id} session={self._session_id}",
                flush=True,
            )
        except Exception as exc:  # pylint: disable=broad-except
            print(
                f"[relay] session setup failed user={self._user_id} session={self._session_id} exc={exc}",
                flush=True,
            )

    def connection_made(self, chan: asyncssh.SSHServerChannel) -> None:
        self._chan = chan
        if self._session:
            self._session.attach_channel(chan)

        print(
            f"[relay] connection_made user={self._user_id} session={self._session_id}",
            flush=True,
        )

    def data_received(self, data: str, datatype: asyncssh.DataType) -> None:
        if isinstance(data, str):
            self._rx_buffer.extend(data.encode())
        else:
            self._rx_buffer.extend(data)

        for frame_type, payload in iter_frames(self._rx_buffer):
            if frame_type == FRAME_TYPE_OUTPUT:
                if self._session is None:
                    print(
                        f"[relay] buffering output frame len={len(payload)} session={self._user_id}:{self._session_id}",
                        flush=True,
                    )
                    self._pending_chunks.append(payload)
                else:
                    print(
                        f"[relay] streaming output frame len={len(payload)} session={self._user_id}:{self._session_id}",
                        flush=True,
                    )
                    self._session.append_output(payload)
            elif frame_type == FRAME_TYPE_RESIZE:
                if len(payload) != 4:
                    print(
                        f"[relay] invalid resize payload len={len(payload)} session={self._user_id}:{self._session_id}",
                        flush=True,
                    )
                    continue

                rows, cols = struct.unpack("!HH", payload)
                if rows <= 0 or cols <= 0:
                    continue

                if self._session is None:
                    self._pending_resize = (cols, rows)
                else:
                    self._session.update_size(cols, rows)
            else:
                print(
                    f"[relay] unknown frame type={frame_type} len={len(payload)} session={self._user_id}:{self._session_id}",
                    flush=True,
                )
                continue

    def connection_lost(self, exc):
        if self._session:
            self._session.detach_channel()
            asyncio.create_task(
                self._manager.end_session(
                    self._session.user_id,
                    self._session.session_id,
                )
            )
        print(
            f"[relay] connection_lost user={self._user_id} session={self._session_id} exc={exc}",
            flush=True,
        )
        self._chan = None
