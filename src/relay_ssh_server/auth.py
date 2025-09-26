"""Helpers for validating API keys within the relay server."""

from __future__ import annotations

import hashlib

from jose import JWTError, jwt

from shared.config import settings


class RelayAuthError(Exception):
    """Raised when an API key cannot be validated."""


def hash_api_key(api_key: str) -> str:
    """Return a SHA256 hash so we never store raw API keys."""

    return hashlib.sha256(api_key.encode("utf-8")).hexdigest()


def decode_api_key(api_key: str) -> str:
    """Validate an API key JWT and return the owning user id."""

    if not api_key:
        raise RelayAuthError("Missing API key")

    if not settings.jwt_public_key:
        raise RelayAuthError("JWT public key not configured")

    try:
        payload = jwt.decode(api_key, settings.jwt_public_key, algorithms=["RS256"])
    except JWTError as exc:  # pragma: no cover - defensive logging path
        raise RelayAuthError(f"Invalid API key: {exc}") from exc

    user_id = payload.get("sub")
    if not user_id:
        raise RelayAuthError("API key missing subject claim")

    return str(user_id)
