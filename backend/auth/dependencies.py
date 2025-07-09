import sys
from pathlib import Path
from uuid import UUID

# Add parent directory to path to import shared module
sys.path.append(str(Path(__file__).parent.parent.parent))

from fastapi import Depends, HTTPException, Request
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from shared.database.models import User
from shared.database.session import get_db
from sqlalchemy.orm import Session

from .supabase_client import get_supabase_client

security = HTTPBearer(auto_error=False)  # Don't auto-error so we can check cookies


class AuthError(HTTPException):
    def __init__(self, detail: str):
        super().__init__(status_code=401, detail=detail)


def get_token_from_request(
    request: Request,
    credentials: HTTPAuthorizationCredentials | None = None,
) -> str | None:
    """Extract token from either Authorization header or session cookie"""
    # First try Authorization header
    if credentials and credentials.credentials:
        return credentials.credentials

    # Then try session cookie
    session_token = request.cookies.get("session_token")
    if session_token:
        return session_token

    return None


async def get_current_user_id(
    request: Request,
    credentials: HTTPAuthorizationCredentials | None = Depends(security),
) -> UUID:
    """Extract and verify user ID from Supabase JWT token (header or cookie)"""
    token = get_token_from_request(request, credentials)

    if not token:
        raise AuthError("No authentication token provided")

    try:
        # Use anon client to verify user tokens (not service role)
        from .supabase_client import get_supabase_anon_client

        supabase = get_supabase_anon_client()

        # Verify the JWT token with Supabase
        user_response = supabase.auth.get_user(token)

        if not user_response or not user_response.user:
            raise AuthError("Invalid authentication token")

        return UUID(user_response.user.id)

    except Exception as e:
        raise AuthError(f"Could not validate credentials: {str(e)}")


async def get_current_user(
    user_id: UUID = Depends(get_current_user_id), db: Session = Depends(get_db)
) -> User:
    """Get current user from database"""
    user = db.query(User).filter(User.id == user_id).first()

    if not user:
        # If user doesn't exist in our DB, create them
        # This handles the case where a user signs up via Supabase
        # but hasn't been synced to our database yet
        service_supabase = get_supabase_client()

        try:
            # Get user info from Supabase using service role
            auth_user = service_supabase.auth.admin.get_user_by_id(str(user_id))

            if auth_user and auth_user.user:
                user = User(
                    id=user_id,
                    email=auth_user.user.email,
                    display_name=auth_user.user.user_metadata.get("display_name"),
                )
                db.add(user)
                db.commit()
                db.refresh(user)
            else:
                raise AuthError("User not found")
        except Exception as e:
            raise AuthError(f"Could not create user: {str(e)}")

    return user


async def get_optional_current_user(
    request: Request,
    credentials: HTTPAuthorizationCredentials | None = Depends(security),
    db: Session = Depends(get_db),
) -> User | None:
    """Get current user if authenticated, otherwise return None"""
    token = get_token_from_request(request, credentials)

    if not token:
        return None

    try:
        # Verify token manually since get_current_user_id requires authentication
        from .supabase_client import get_supabase_anon_client

        supabase = get_supabase_anon_client()
        user_response = supabase.auth.get_user(token)

        if not user_response or not user_response.user:
            return None

        user_id = UUID(user_response.user.id)

        # Get user from database
        user = db.query(User).filter(User.id == user_id).first()

        if not user:
            # Create user if doesn't exist
            user = User(
                id=user_id,
                email=user_response.user.email,
                display_name=user_response.user.user_metadata.get("display_name"),
            )
            db.add(user)
            db.commit()
            db.refresh(user)

        return user

    except Exception:
        return None
