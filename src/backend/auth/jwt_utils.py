import hashlib
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

# Add parent directory to path to import shared module
sys.path.append(str(Path(__file__).parent.parent.parent))

from jose import JWTError, jwt
from shared.config.settings import settings


def create_api_key_jwt(
    user_id: str,
    expires_in_days: int | None = None,
    additional_claims: dict[str, Any] | None = None,
) -> str:
    """
    Create a JWT token for API key authentication.

    Args:
        user_id: User's UUID as string
        expires_in_days: Token expiration in days (None for no expiration)
        additional_claims: Extra claims to include in the token

    Returns:
        JWT token string
    """
    if not settings.jwt_private_key:
        raise ValueError("JWT_PRIVATE_KEY not configured")

    now = datetime.now(timezone.utc)

    payload = {
        "sub": user_id,
        "iat": int(now.timestamp()),
    }

    # Only add expiration if specified
    if expires_in_days is not None:
        expires_at = now + timedelta(days=expires_in_days)
        payload["exp"] = int(expires_at.timestamp())

    if additional_claims:
        payload.update(additional_claims)

    token = jwt.encode(payload, settings.jwt_private_key, algorithm="RS256")

    return token


def verify_api_key_jwt(token: str) -> dict[str, Any]:
    """
    Verify and decode a JWT API key token.

    Args:
        token: JWT token string

    Returns:
        Decoded token payload

    Raises:
        JWTError: If token is invalid, expired, or malformed
    """
    if not settings.jwt_public_key:
        raise ValueError("JWT_PUBLIC_KEY not configured")

    try:
        payload = jwt.decode(
            token,
            settings.jwt_public_key,
            algorithms=["RS256"],
        )
        return payload
    except JWTError as e:
        raise JWTError(f"Invalid token: {str(e)}")


def get_token_hash(token: str) -> str:
    """
    Generate SHA256 hash of a token for storage.
    We store hashes instead of the actual tokens for security.

    Args:
        token: JWT token string

    Returns:
        SHA256 hash of the token
    """
    return hashlib.sha256(token.encode()).hexdigest()


def extract_user_id_from_token(token: str) -> str:
    """
    Extract user ID from token without full verification.
    Useful for database lookups before verification.

    Args:
        token: JWT token string

    Returns:
        User ID from token subject claim

    Raises:
        JWTError: If token is malformed
    """
    try:
        # Decode without verification (just to extract claims)
        unverified_payload = jwt.get_unverified_claims(token)
        user_id = unverified_payload.get("sub")
        if user_id is None:
            raise JWTError("Token missing subject claim")
        return str(user_id)
    except Exception as e:
        raise JWTError(f"Cannot extract user ID: {str(e)}")
