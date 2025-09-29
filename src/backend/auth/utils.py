import sys
from pathlib import Path
from uuid import UUID

# Add parent directory to path to import shared module
sys.path.append(str(Path(__file__).parent.parent.parent))

from shared.database.models import User
from sqlalchemy.orm import Session

from shared.auth import get_supabase_service_client


def sync_user_from_supabase(user_id: UUID, db: Session) -> User:
    """Sync user data from Supabase to our database"""
    supabase = get_supabase_service_client()

    # Get user from Supabase
    auth_user = supabase.auth.admin.get_user_by_id(str(user_id))

    if not auth_user or not auth_user.user:
        raise ValueError(f"User {user_id} not found in Supabase")

    # Check if user exists in our DB
    user = db.query(User).filter(User.id == user_id).first()

    if user:
        # Update existing user
        if auth_user.user.email:
            user.email = auth_user.user.email
        user.display_name = auth_user.user.user_metadata.get(
            "display_name", user.display_name
        )
    else:
        # Create new user
        user = User(
            id=user_id,
            email=auth_user.user.email,
            display_name=auth_user.user.user_metadata.get("display_name"),
        )
        db.add(user)

    db.commit()
    db.refresh(user)

    return user


def update_user_profile(user_id: UUID, display_name: str | None, db: Session) -> User:
    """Update user profile information"""
    user = db.query(User).filter(User.id == user_id).first()

    if not user:
        raise ValueError(f"User {user_id} not found")

    if display_name is not None:
        user.display_name = display_name

    db.commit()
    db.refresh(user)

    # Also update in Supabase metadata
    supabase = get_supabase_service_client()
    supabase.auth.admin.update_user_by_id(
        str(user_id), {"user_metadata": {"display_name": display_name}}
    )

    return user
