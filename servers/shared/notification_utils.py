"""Notification utilities for sending push, email, and SMS notifications."""

import logging
from uuid import UUID
from sqlalchemy.orm import Session

from shared.database import User, AgentInstance
from .notifications import push_service
from .twilio_service import twilio_service

logger = logging.getLogger(__name__)


async def send_message_notifications(
    db: Session,
    instance_id: UUID,
    content: str,
    requires_user_input: bool,
    send_email: bool | None = None,
    send_sms: bool | None = None,
    send_push: bool | None = None,
) -> None:
    """Send notifications for a message (either step or question).

    Args:
        db: Database session
        instance_id: Agent instance ID
        content: Message content
        requires_user_input: Whether this message requires user input
        send_email: Override email notification preference
        send_sms: Override SMS notification preference
        send_push: Override push notification preference
    """
    # Get instance and user
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
    if not instance:
        logger.warning(f"Instance {instance_id} not found for notifications")
        return

    user = db.query(User).filter(User.id == instance.user_id).first()
    if not user:
        logger.warning(f"User {instance.user_id} not found for notifications")
        return

    agent_name = instance.user_agent.name if instance.user_agent else "Agent"

    # Determine notification preferences based on message type
    if requires_user_input:
        # For questions: respect user preferences
        should_send_push = (
            send_push if send_push is not None else user.push_notifications_enabled
        )
        should_send_email = (
            send_email if send_email is not None else user.email_notifications_enabled
        )
        should_send_sms = (
            send_sms if send_sms is not None else user.sms_notifications_enabled
        )
    else:
        # For steps: notifications default to False unless explicitly enabled
        should_send_push = send_push if send_push is not None else False
        should_send_email = send_email if send_email is not None else False
        should_send_sms = send_sms if send_sms is not None else False

    # Send push notification if enabled
    if should_send_push:
        try:
            if requires_user_input:
                await push_service.send_question_notification(
                    db=db,
                    user_id=instance.user_id,
                    instance_id=str(instance.id),
                    agent_name=agent_name,
                    question_text=content,
                )
            else:
                await push_service.send_step_notification(
                    db=db,
                    user_id=instance.user_id,
                    instance_id=str(instance.id),
                    agent_name=agent_name,
                    step_description=content,
                )
        except Exception as e:
            logger.error(f"Failed to send push notification: {e}")

    # Send Twilio notifications if enabled
    if should_send_email or should_send_sms:
        try:
            if requires_user_input:
                await twilio_service.send_question_notification(
                    db=db,
                    user_id=instance.user_id,
                    instance_id=str(instance.id),
                    agent_name=agent_name,
                    question_text=content,
                    send_email=should_send_email,
                    send_sms=should_send_sms,
                )
            else:
                await twilio_service.send_step_notification(
                    db=db,
                    user_id=instance.user_id,
                    instance_id=str(instance.id),
                    agent_name=agent_name,
                    step_description=content,
                    send_email=should_send_email,
                    send_sms=should_send_sms,
                )
        except Exception as e:
            logger.error(f"Failed to send Twilio notification: {e}")
