"""Shared helpers for validating Supabase-issued access tokens."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Dict, Tuple
from uuid import UUID

from .supabase import get_supabase_anon_client


class TokenVerificationError(Exception):
    """Raised when a Supabase token cannot be verified."""


@dataclass(slots=True)
class TokenClaims:
    """Minimal set of fields we care about after verification."""

    user_id: UUID


_TOKEN_CACHE: Dict[str, Tuple[TokenClaims, datetime]] = {}
_CACHE_TTL = timedelta(minutes=5)
_CACHE_MAX_SIZE = 2048


def verify_supabase_access_token(token: str) -> TokenClaims:
    """Validate a Supabase access token and return basic claims.

    The verification uses the anon client so it matches the frontend-issued
    credentials. Results are cached for a short window to avoid repeated calls
    for the same token during rapid polling.
    """

    if not token:
        raise TokenVerificationError("Missing access token")

    cached = _TOKEN_CACHE.get(token)
    if cached:
        claims, expires_at = cached
        if datetime.utcnow() < expires_at:
            return claims
        del _TOKEN_CACHE[token]

    try:
        supabase = get_supabase_anon_client()
        user_response = supabase.auth.get_user(token)
    except Exception as exc:  # pragma: no cover - defensive path
        raise TokenVerificationError(f"Failed to validate token: {exc}") from exc

    if not user_response or not getattr(user_response, "user", None):
        raise TokenVerificationError("Invalid access token")

    try:
        user_id = UUID(user_response.user.id)
    except Exception as exc:  # pragma: no cover - malformed data
        raise TokenVerificationError("Token missing valid subject") from exc

    claims = TokenClaims(user_id=user_id)
    _TOKEN_CACHE[token] = (claims, datetime.utcnow() + _CACHE_TTL)

    if len(_TOKEN_CACHE) > _CACHE_MAX_SIZE:
        _TOKEN_CACHE.clear()

    return claims
