"""Omnara - Agent Dashboard and Python SDK

This package provides:
1. MCP Server for agent communication (omnara CLI command)
2. Python SDK for interacting with the Omnara API
"""

# Import SDK components for easy access
from .sdk.client import OmnaraClient
from .sdk.async_client import AsyncOmnaraClient
from .sdk.exceptions import (
    OmnaraError,
    AuthenticationError,
    TimeoutError,
    APIError,
)

try:
    from importlib.metadata import version

    __version__ = version("omnara")
except Exception:
    __version__ = "unknown"
__all__ = [
    "OmnaraClient",
    "AsyncOmnaraClient",
    "OmnaraError",
    "AuthenticationError",
    "TimeoutError",
    "APIError",
]
