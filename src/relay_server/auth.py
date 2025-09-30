"""Helpers for validating relay authentication credentials."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass

from jose import JWTError, jwt

from shared.config import settings
from shared.auth.tokens import (
    TokenVerificationError,
    verify_supabase_access_token,
)


class RelayAuthError(Exception):
    """Raised when an API key cannot be validated."""


@dataclass(slots=True)
class RelayCredentials:
    """Represents authenticated viewer identity for the relay."""

    user_id: str
    api_key_hash: str | None


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


def decode_supabase_token(token: str) -> str:
    """Validate a Supabase access token and return the owning user id."""

    try:
        claims = verify_supabase_access_token(token)
    except TokenVerificationError as exc:  # pragma: no cover - defensive logging path
        raise RelayAuthError(str(exc)) from exc

    return str(claims.user_id)


def build_credentials_from_api_key(api_key: str) -> RelayCredentials:
    """Decode an API key and return relay credentials."""

    user_id = decode_api_key(api_key)
    return RelayCredentials(user_id=user_id, api_key_hash=hash_api_key(api_key))


def build_credentials_from_supabase(token: str) -> RelayCredentials:
    """Decode a Supabase token and return relay credentials."""

    user_id = decode_supabase_token(token)
    return RelayCredentials(user_id=user_id, api_key_hash=None)
