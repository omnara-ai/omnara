"""FastAPI-based relay WebSocket server."""

from __future__ import annotations

import asyncio
import logging
from contextlib import asynccontextmanager
from typing import Optional

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

from relay_server.routes import create_relay_router
from relay_server.config import RelaySettings
from relay_server.sessions import SessionManager

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Lifespan manager for the relay server."""
    logger.info("Relay server starting up")
    logger.info("WebSocket endpoints: /agent (agents), /terminal (viewers)")

    # Start background task to reap inactive sessions
    settings: RelaySettings = app.state.settings
    manager: SessionManager = app.state.manager

    async def reap_loop() -> None:
        while True:
            await asyncio.sleep(settings.heartbeat_interval_seconds)
            await manager.reap_inactive()

    reap_task = asyncio.create_task(reap_loop())

    yield

    logger.info("Shutting down relay server")
    reap_task.cancel()
    try:
        await reap_task
    except asyncio.CancelledError:
        pass


def create_app(settings: Optional[RelaySettings] = None) -> FastAPI:
    """Create and configure the FastAPI relay app."""
    settings = settings or RelaySettings.from_env()

    manager = SessionManager(
        history_limit=settings.history_size_bytes,
        heartbeat_miss_limit=settings.heartbeat_miss_limit,
        heartbeat_interval=settings.heartbeat_interval_seconds,
        ended_retention_seconds=settings.ended_retention_seconds,
    )

    app = FastAPI(
        title="Omnara Terminal Relay",
        description="WebSocket relay for terminal streaming",
        version="1.0.0",
        lifespan=lifespan,
    )

    # Store config in app state
    app.state.settings = settings
    app.state.manager = manager

    # Configure CORS
    app.add_middleware(
        CORSMiddleware,
        allow_origins=settings.allowed_origins,
        allow_credentials=True,
        allow_methods=["*"],
        allow_headers=["*"],
    )

    # Mount relay routes
    app.include_router(create_relay_router(manager))

    @app.get("/")
    async def root():
        return {
            "message": "Omnara Terminal Relay",
            "version": "1.0.0",
            "endpoints": {
                "agent": "/agent (WebSocket for agent connections)",
                "viewer": "/terminal (WebSocket for terminal viewers)",
                "sessions": "/api/v1/sessions (List active sessions)",
            },
        }

    @app.get("/health")
    async def health_check():
        return {"status": "healthy", "server": "relay"}

    return app


def main() -> None:
    """Console entry point."""
    import uvicorn

    settings = RelaySettings.from_env()
    app = create_app(settings)

    logger.info(
        f"Starting relay server on {settings.websocket_host}:{settings.websocket_port}"
    )

    uvicorn.run(
        app,
        host=settings.websocket_host,
        port=settings.websocket_port,
        log_level="info",
        ws_ping_interval=None,  # Disable automatic WebSocket pings
        ws_ping_timeout=None,
    )


if __name__ == "__main__":
    main()
