"""Top-level entry point for the relay SSH server."""

from __future__ import annotations

import asyncio
from typing import Optional

import asyncssh
from aiohttp import web

from .config import RelaySettings
from .sessions import SessionManager
from .ssh_server import RelaySSHServer
from .websocket import WebsocketRouter


async def run_async(settings: Optional[RelaySettings] = None) -> None:
    """Run the SSH relay server until cancelled."""

    settings = settings or RelaySettings.from_env()
    manager = SessionManager(
        history_limit=settings.history_size_bytes,
        heartbeat_miss_limit=settings.heartbeat_miss_limit,
        heartbeat_interval=settings.heartbeat_interval_seconds,
        ended_retention_seconds=settings.ended_retention_seconds,
    )

    if settings.host_key_path:
        server_host_keys = [settings.host_key_path]
    else:
        server_host_keys = [asyncssh.generate_private_key("ssh-ed25519")]

    server = await asyncssh.create_server(  # type: ignore[call-arg]
        lambda: RelaySSHServer(manager),
        host=settings.ssh_host,
        port=settings.ssh_port,
        server_host_keys=server_host_keys,
        encoding=None,
        line_editor=False,
    )

    app = web.Application()
    ws_router = WebsocketRouter(manager)
    app.router.add_get("/terminal", ws_router.handle)
    app.router.add_get("/api/v1/sessions", ws_router.list_sessions)

    runner = web.AppRunner(app)
    await runner.setup()
    site = web.TCPSite(runner, settings.websocket_host, settings.websocket_port)
    await site.start()

    stop_event = asyncio.Event()

    print(
        f"Relay SSH server started on ssh://{settings.ssh_host}:{settings.ssh_port} "
        f"and ws://{settings.websocket_host}:{settings.websocket_port}",
        flush=True,
    )
    print("Press Ctrl+C to stop the relay", flush=True)

    async def reap_loop() -> None:
        while True:
            await asyncio.sleep(settings.heartbeat_interval_seconds)
            await manager.reap_inactive()

    asyncio.create_task(reap_loop())

    try:
        await stop_event.wait()
    finally:
        server.close()
        await server.wait_closed()
        await runner.cleanup()


def run_sync() -> None:
    """Convenience wrapper used by synchronous entry points."""

    asyncio.run(run_async())


def main() -> None:
    """Console entry point."""

    run_sync()


if __name__ == "__main__":
    main()
