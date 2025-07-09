"""FastAPI server for Agent Dashboard API endpoints.

This server provides RESTful endpoints that mirror the MCP server tools,
using the same JWT authentication mechanism as the MCP server.
"""

import logging
from contextlib import asynccontextmanager

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
from shared.config import settings

from .routers import agent_router

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)


@asynccontextmanager
async def lifespan(app: FastAPI):
    """Startup and shutdown events."""
    # Startup
    logger.info("FastAPI server starting up")
    yield
    # Shutdown
    logger.info("Shutting down FastAPI server")


# Create FastAPI app
app = FastAPI(
    title="Agent Dashboard API",
    description="RESTful API for agent monitoring and interaction",
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

# Include routers
app.include_router(agent_router, prefix="/api/v1")


@app.get("/")
async def root():
    """Root endpoint."""
    return {
        "message": "Agent Dashboard FastAPI Server",
        "version": "1.0.0",
        "docs": "/docs",
    }


@app.get("/health")
async def health_check():
    """Health check endpoint."""
    return {"status": "healthy"}


if __name__ == "__main__":
    import uvicorn

    # Use a different port than the backend API
    port = int(settings.api_port) + 1000  # e.g., 9000 if api_port is 8000
    uvicorn.run("servers.fastapi.main:app", host="0.0.0.0", port=port, reload=True)
