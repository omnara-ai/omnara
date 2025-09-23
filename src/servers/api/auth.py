"""Authentication dependencies for FastAPI server.

Uses the same JWT authentication as the MCP server.
"""

from typing import Annotated

from fastapi import Depends, HTTPException, status
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from jose import JWTError, jwt
from shared.config import settings

# Bearer token security scheme
security = HTTPBearer()


async def verify_token(
    credentials: Annotated[HTTPAuthorizationCredentials, Depends(security)],
) -> dict:
    """Verify JWT token and return decoded payload.

    Args:
        credentials: Bearer token from Authorization header

    Returns:
        Decoded JWT payload including user_id

    Raises:
        HTTPException: If token is invalid or missing
    """
    if not settings.jwt_public_key:
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="JWT public key not configured",
        )

    token = credentials.credentials

    try:
        # Decode and verify the JWT token
        payload = jwt.decode(token, settings.jwt_public_key, algorithms=["RS256"])
        return payload
    except JWTError as e:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail=f"Invalid token: {str(e)}",
            headers={"WWW-Authenticate": "Bearer"},
        )


async def get_current_user_id(
    token_payload: Annotated[dict, Depends(verify_token)],
) -> str:
    """Extract user ID from verified token payload.

    Args:
        token_payload: Decoded JWT payload

    Returns:
        User ID string

    Raises:
        HTTPException: If user ID is missing from token
    """
    user_id = token_payload.get("sub")
    if not user_id:
        raise HTTPException(
            status_code=status.HTTP_401_UNAUTHORIZED,
            detail="Token missing user ID",
            headers={"WWW-Authenticate": "Bearer"},
        )
    return user_id
