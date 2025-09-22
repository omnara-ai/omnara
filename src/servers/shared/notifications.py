"""Push notification service using Expo Push API"""

import asyncio
import logging
from datetime import datetime, timezone
from typing import Dict, Any, Optional
from uuid import UUID
from sqlalchemy.orm import Session
from exponent_server_sdk import (
    PushClient,
    PushMessage,
    PushServerError,
    PushTicketError,
    DeviceNotRegisteredError,
)
import requests.exceptions

from shared.database import PushToken, User
from .notification_base import NotificationServiceBase

logger = logging.getLogger(__name__)


class PushNotificationService(NotificationServiceBase):
    """Service for sending push notifications via Expo"""

    def __init__(self):
        self.client = PushClient()

    async def send_notification(
        self,
        db: Session,
        user_id: UUID,
        title: str,
        body: str,
        data: Optional[Dict[str, Any]] = None,
    ) -> bool:
        """Send push notification to all user's devices"""
        try:
            # First check if user has push notifications enabled
            user = db.query(User).filter(User.id == user_id).first()
            if not user or not user.push_notifications_enabled:
                logger.info(f"Push notifications disabled for user {user_id}")
                return False

            # Get user's active push tokens
            tokens = (
                db.query(PushToken)
                .filter(PushToken.user_id == user_id, PushToken.is_active)
                .all()
            )

            if not tokens:
                logger.info(f"No push tokens found for user {user_id}")
                return False

            # Deduplicate tokens to prevent sending same notification multiple times
            unique_tokens = {}
            for token in tokens:
                unique_tokens[token.token] = token

            logger.info(
                f"Found {len(tokens)} total tokens, {len(unique_tokens)} unique tokens for user {user_id}"
            )

            # Prepare messages for Expo
            messages = []
            for token in unique_tokens.values():
                # Validate token format
                if not PushClient.is_exponent_push_token(token.token):
                    logger.warning(f"Invalid Expo push token: {token.token}")
                    continue

                message = PushMessage(
                    to=token.token,
                    title=title,
                    body=body,
                    data=data or {},
                    sound="default",
                    priority="high",
                    channel_id="agent-questions",
                    ttl=None,  # Use platform defaults (1 month) - agent questions should remain accessible
                    expiration=None,
                    badge=None,  # Don't modify app badge count
                    category=None,
                    display_in_foreground=True,
                    subtitle=None,
                    mutable_content=False,
                )
                messages.append(message)

            if not messages:
                logger.warning("No valid push tokens to send to")
                return False

            # Send to Expo Push API in chunks with retry logic
            max_retries = 3
            for attempt in range(max_retries):
                try:
                    if attempt > 0:
                        logger.info(
                            f"Push notification retry attempt {attempt + 1} of {max_retries}"
                        )

                    # Send messages in batches (Expo recommends max 100 per batch)
                    for chunk in self._chunks(messages, 100):
                        response = self.client.publish_multiple(chunk)

                        # Check for errors in the response
                        for push_ticket in response:
                            if (
                                hasattr(push_ticket, "status")
                                and push_ticket.status == "error"
                            ):
                                logger.error(
                                    f"Push notification error: {getattr(push_ticket, 'message', 'Unknown error')}"
                                )

                    logger.info(
                        f"Successfully sent push notifications to user {user_id}"
                    )
                    return True

                except DeviceNotRegisteredError as e:
                    logger.error(f"Device not registered, deactivating token: {str(e)}")
                    # Mark token as inactive
                    for token in tokens:
                        if token.token in str(e):
                            token.is_active = False
                            token.updated_at = datetime.now(timezone.utc)
                            db.commit()
                    return False
                except PushTicketError as e:
                    logger.error(f"Push ticket error: {str(e)}")
                    return False
                except Exception as e:
                    # Check if this is a connection-related error that should be retried
                    # This includes ConnectionError, OSError, requests.exceptions.RequestException, etc.
                    # We check the exception type and its base classes
                    error_type = type(e)
                    is_connection_error = (
                        isinstance(
                            e,
                            (
                                ConnectionError,
                                OSError,
                                requests.exceptions.RequestException,
                                PushServerError,
                            ),
                        )
                        or any(
                            issubclass(error_type, exc_type)
                            for exc_type in [ConnectionError, OSError]
                        )
                        or "connection" in str(e).lower()
                        or "reset" in str(e).lower()
                    )

                    if is_connection_error and attempt < max_retries - 1:
                        wait_time = 2**attempt  # Exponential backoff: 1s, 2s, 4s
                        logger.warning(
                            f"Push notification attempt {attempt + 1} failed, retrying in {wait_time}s: {type(e).__name__}: {e}"
                        )
                        await asyncio.sleep(wait_time)
                        continue
                    elif is_connection_error:
                        logger.error(
                            f"Push server error after {max_retries} attempts: {type(e).__name__}: {e}"
                        )
                        return False
                    else:
                        # Non-connection error, don't retry
                        logger.error(
                            f"Unexpected error sending push notification: {type(e).__name__}: {e}"
                        )
                        return False

            # If we get here, all retry attempts were exhausted
            return False

        except Exception as e:
            logger.error(
                f"Unexpected error in send_notification: {type(e).__name__}: {e}"
            )
            return False

    async def send_question_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        question_text: str,
    ) -> bool:
        """Send notification for new agent question"""
        # Format agent name for display
        display_name = agent_name.replace("_", " ").title()
        title = f"{display_name} needs your input"

        # Truncate question text for notification
        body = question_text
        if len(body) > 100:
            body = body[:97] + "..."

        data = {
            "type": "new_question",
            "instanceId": instance_id,
        }

        return await self.send_notification(
            db=db,
            user_id=user_id,
            title=title,
            body=body,
            data=data,
        )

    async def send_step_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        step_description: str,
    ) -> bool:
        """Send notification for new agent step"""
        # Format agent name for display
        display_name = agent_name.replace("_", " ").title()
        title = f"{display_name} - New Step"

        # Truncate step description for notification
        body = step_description
        if len(body) > 100:
            body = body[:97] + "..."

        data = {
            "type": "new_step",
            "instanceId": instance_id,
        }

        return await self.send_notification(
            db=db,
            user_id=user_id,
            title=title,
            body=body,
            data=data,
        )

    @staticmethod
    def _chunks(lst, n):
        """Yield successive n-sized chunks from lst."""
        for i in range(0, len(lst), n):
            yield lst[i : i + n]


# Singleton instance
push_service = PushNotificationService()
