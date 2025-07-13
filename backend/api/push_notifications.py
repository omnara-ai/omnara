"""Push notification endpoints"""

from typing import List
from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy.orm import Session
from pydantic import BaseModel
from uuid import UUID
from datetime import datetime, timezone

from backend.auth.dependencies import get_current_user_id
from shared.database.session import get_db
from shared.database import PushToken

router = APIRouter(prefix="/push", tags=["push_notifications"])


class RegisterPushTokenRequest(BaseModel):
    token: str
    platform: str  # 'ios' or 'android'


class PushTokenResponse(BaseModel):
    id: UUID
    token: str
    platform: str
    is_active: bool


@router.post("/register", response_model=dict)
def register_push_token(
    request: RegisterPushTokenRequest,
    user_id: UUID = Depends(get_current_user_id),
    db: Session = Depends(get_db),
):
    """Register a push notification token for the current user"""
    try:
        # Check if token already exists
        existing = db.query(PushToken).filter(PushToken.token == request.token).first()

        if existing:
            # Update existing token
            existing.user_id = user_id
            existing.platform = request.platform
            existing.is_active = True
            existing.updated_at = datetime.now(timezone.utc)
        else:
            # Create new token
            push_token = PushToken(
                user_id=user_id,
                token=request.token,
                platform=request.platform,
                is_active=True,
            )
            db.add(push_token)

        db.commit()
        return {"success": True, "message": "Push token registered successfully"}
    except Exception as e:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(e))


@router.delete("/deactivate/{token}")
def deactivate_token(
    token: str,
    user_id: UUID = Depends(get_current_user_id),
    db: Session = Depends(get_db),
):
    """Deactivate a push notification token"""
    try:
        push_token = (
            db.query(PushToken)
            .filter(PushToken.user_id == user_id, PushToken.token == token)
            .first()
        )

        if push_token:
            push_token.is_active = False
            push_token.updated_at = datetime.now(timezone.utc)
            db.commit()

        return {"success": True, "message": "Push token deactivated"}
    except Exception as e:
        db.rollback()
        raise HTTPException(status_code=400, detail=str(e))


@router.get("/tokens", response_model=List[PushTokenResponse])
def get_my_push_tokens(
    user_id: UUID = Depends(get_current_user_id),
    db: Session = Depends(get_db),
):
    """Get all push tokens for the current user"""
    tokens = (
        db.query(PushToken)
        .filter(PushToken.user_id == user_id, PushToken.is_active)
        .all()
    )

    return [
        PushTokenResponse(
            id=token.id,
            token=token.token,
            platform=token.platform,
            is_active=token.is_active,
        )
        for token in tokens
    ]
