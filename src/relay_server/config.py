"""Configuration helpers for the relay WebSocket server."""

from __future__ import annotations

from dataclasses import dataclass, field
import os


@dataclass(slots=True)
class RelaySettings:
    """Runtime configuration sourced from environment variables."""

    websocket_host: str = "0.0.0.0"
    websocket_port: int = 8787
    history_size_bytes: int = 1024 * 1024
    heartbeat_interval_seconds: int = 10
    heartbeat_miss_limit: int = 3
    ended_retention_seconds: int = 15 * 60
    allowed_origins: list[str] = field(
        default_factory=lambda: [
            "https://claude.omnara.com",
            "https://omnara.ai",
            "http://localhost:5173",
            "http://127.0.0.1:5173",
            "null",
        ]
    )

    @classmethod
    def from_env(cls) -> "RelaySettings":
        """Create settings object using environment overrides."""

        def _int(name: str, default: int) -> int:
            raw = os.getenv(name)
            try:
                return int(raw) if raw is not None else default
            except ValueError:
                return default

        defaults = cls()

        def _list(name: str, default: list[str]) -> list[str]:
            raw = os.getenv(name)
            if raw is None:
                return list(default)
            items = [item.strip() for item in raw.split(",") if item.strip()]
            return items if items else list(default)

        return cls(
            websocket_host=os.getenv("OMNARA_RELAY_WS_HOST", defaults.websocket_host),
            websocket_port=_int("OMNARA_RELAY_WS_PORT", defaults.websocket_port),
            history_size_bytes=_int(
                "OMNARA_RELAY_HISTORY_BYTES", defaults.history_size_bytes
            ),
            heartbeat_interval_seconds=_int(
                "OMNARA_RELAY_HEARTBEAT_INTERVAL", defaults.heartbeat_interval_seconds
            ),
            heartbeat_miss_limit=_int(
                "OMNARA_RELAY_HEARTBEAT_MISS_LIMIT", defaults.heartbeat_miss_limit
            ),
            ended_retention_seconds=_int(
                "OMNARA_RELAY_ENDED_RETENTION", defaults.ended_retention_seconds
            ),
            allowed_origins=_list(
                "OMNARA_RELAY_ALLOWED_ORIGINS", defaults.allowed_origins
            ),
        )
