"""
User Settings API endpoints for managing notification preferences and other user settings.
"""

from fastapi import APIRouter, Depends, HTTPException
from shared.database.models import User
from shared.database.session import get_db
from sqlalchemy.orm import Session

from ..auth.dependencies import get_current_user
from ..models import (
    UserNotificationSettingsRequest,
    UserNotificationSettingsResponse,
)

router = APIRouter(tags=["user-settings"])


@router.get(
    "/user/notification-settings", response_model=UserNotificationSettingsResponse
)
async def get_notification_settings(current_user: User = Depends(get_current_user)):
    """Get current user's notification settings"""
    return UserNotificationSettingsResponse(
        push_notifications_enabled=current_user.push_notifications_enabled,
        email_notifications_enabled=current_user.email_notifications_enabled,
        sms_notifications_enabled=current_user.sms_notifications_enabled,
        phone_number=current_user.phone_number,
        notification_email=current_user.notification_email or current_user.email,
    )


@router.put(
    "/user/notification-settings", response_model=UserNotificationSettingsResponse
)
async def update_notification_settings(
    request: UserNotificationSettingsRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Update current user's notification settings"""
    if request.push_notifications_enabled is not None:
        current_user.push_notifications_enabled = request.push_notifications_enabled

    if request.email_notifications_enabled is not None:
        current_user.email_notifications_enabled = request.email_notifications_enabled

    if request.sms_notifications_enabled is not None:
        current_user.sms_notifications_enabled = request.sms_notifications_enabled

    if request.phone_number is not None:
        # Basic validation for phone number format (E.164)
        if request.phone_number and not request.phone_number.startswith("+"):
            raise HTTPException(
                status_code=400,
                detail="Phone number must be in E.164 format (e.g., +1234567890)",
            )
        current_user.phone_number = request.phone_number

    if request.notification_email is not None:
        # If empty string, set to None to use default email
        current_user.notification_email = request.notification_email or None

    db.commit()
    db.refresh(current_user)

    return UserNotificationSettingsResponse(
        push_notifications_enabled=current_user.push_notifications_enabled,
        email_notifications_enabled=current_user.email_notifications_enabled,
        sms_notifications_enabled=current_user.sms_notifications_enabled,
        phone_number=current_user.phone_number,
        notification_email=current_user.notification_email or current_user.email,
    )


@router.post("/user/test-notification")
async def test_notification(
    notification_type: str = "all",  # "push", "sms", "email", or "all"
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Send a test notification to verify settings"""
    results = {"push": False, "sms": False, "email": False}

    # Test push notification
    if notification_type in ["push", "all"] and current_user.push_notifications_enabled:
        try:
            from servers.shared.notifications import push_service

            results["push"] = await push_service.send_notification(
                db=db,
                user_id=current_user.id,
                title="Test Notification",
                body="This is a test notification from Omnara.",
            )
        except Exception:
            results["push"] = False

    # Test email notification
    if (
        notification_type in ["email", "all"]
        and current_user.email_notifications_enabled
    ):
        try:
            from servers.shared.twilio_service import twilio_service

            email_results = twilio_service.send_notification(
                db=db,
                user_id=current_user.id,
                title="Test Notification",
                body="This is a test notification from Omnara. If you received this, your email notification settings are working correctly.",
                send_email=True,
                send_sms=False,
            )
            results["email"] = email_results.get("email", False)
        except Exception:
            pass

    # Test SMS notification
    if notification_type in ["sms", "all"] and current_user.sms_notifications_enabled:
        try:
            from servers.shared.twilio_service import twilio_service

            sms_results = twilio_service.send_notification(
                db=db,
                user_id=current_user.id,
                title="Test Notification",
                body="Test notification body",
                sms_body="Omnara test notification. Your settings are working!",
                send_email=False,
                send_sms=True,
            )
            results["sms"] = sms_results.get("sms", False)
        except Exception:
            pass

    return {
        "status": "success",
        "results": results,
        "message": "Test notifications sent. Check your devices.",
    }
