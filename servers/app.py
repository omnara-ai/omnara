"""Unified server combining MCP and FastAPI functionality.

This server provides:
- MCP tools at /mcp/ endpoint (log_step, ask_question, end_session)
- REST API endpoints at /api/v1/*
- Shared JWT authentication for both interfaces
"""

import logging
from contextlib import asynccontextmanager
import traceback

from fastapi import FastAPI, Request, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import JSONResponse
import sentry_sdk
from shared.config import settings

# Import the pre-configured MCP server
from servers.mcp_server.server import mcp

# Import FastAPI routers
from servers.fastapi_server.routers import agent_router

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Initialize Sentry only if DSN is provided
if settings.sentry_dsn:
    sentry_sdk.init(
        dsn=settings.sentry_dsn,
        send_default_pii=True,
        environment=settings.environment,
    )
    logger.info(f"Sentry initialized for {settings.environment} environment")
else:
    logger.info("Sentry DSN not provided, error tracking disabled")

# Get the MCP app with streamable-http transport
mcp_app = mcp.http_app(path="/")


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Combined lifespan for both MCP and FastAPI functionality."""
    # Use the MCP app's lifespan to ensure proper initialization
    async with mcp_app.lifespan(app):
        logger.info("Unified server starting up")
        logger.info("MCP endpoints available at: /mcp/")
        logger.info("REST API endpoints available at: /api/v1/*")
        yield
        logger.info("Shutting down unified server")


# Create FastAPI app with MCP's lifespan
app = FastAPI(
    title="Agent Dashboard Unified Server",
    description="Combined MCP and REST API for agent monitoring and interaction",
    version="1.0.0",
    lifespan=lifespan,
)

# Configure CORS
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],  # Configure appropriately for production
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.exception_handler(Exception)
async def global_exception_handler(request: Request, exc: Exception):
    """Global exception handler that logs all unhandled exceptions."""
    # Log the error with full traceback
    logger.error(f"Unhandled exception in {request.method} {request.url.path}")
    logger.error(f"Exception: {type(exc).__name__}: {str(exc)}")
    logger.error(traceback.format_exc())

    # Re-raise HTTPExceptions to preserve their status codes
    if isinstance(exc, HTTPException):
        raise exc

    # For all other exceptions, return 500
    return JSONResponse(status_code=500, content={"detail": "Internal server error"})


app.include_router(agent_router, prefix="/api/v1")
app.mount("/mcp", mcp_app)


@app.get("/")
async def root():
    """Root endpoint."""
    return {
        "message": "Agent Dashboard Unified Server",
        "version": "1.0.0",
        "endpoints": {
            "mcp": "/mcp/ (MCP tools via Streamable HTTP)",
            "api": "/api/v1/* (REST API endpoints)",
            "docs": "/docs (API documentation)",
        },
    }


@app.get("/health")
async def health_check():
    """Health check endpoint."""
    return {"status": "healthy", "server": "unified"}


def main():
    """Run the unified server."""
    import uvicorn

    # Log configuration for debugging
    logger.info(f"Starting unified server on port: {settings.mcp_server_port}")
    logger.info("Database URL configured.")
    logger.info(
        f"JWT public key configured: {'Yes' if settings.jwt_public_key else 'No'}"
    )

    try:
        uvicorn.run(
            "servers.app:app",
            host="0.0.0.0",
            port=settings.mcp_server_port,
            reload=settings.environment == "development",
        )
    except Exception as e:
        logger.error(f"Failed to start unified server: {e}")
        raise


if __name__ == "__main__":
    main()
