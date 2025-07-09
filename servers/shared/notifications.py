"""Push notification service using Expo Push API"""

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

from shared.database import PushToken

logger = logging.getLogger(__name__)


class PushNotificationService:
    """Service for sending push notifications via Expo"""

    def __init__(self):
        self.client = PushClient()

    def send_notification(
        self,
        db: Session,
        user_id: UUID,
        title: str,
        body: str,
        data: Optional[Dict[str, Any]] = None,
    ) -> bool:
        """Send push notification to all user's devices"""
        try:
            # Get user's active push tokens
            tokens = (
                db.query(PushToken)
                .filter(PushToken.user_id == user_id, PushToken.is_active)
                .all()
            )

            if not tokens:
                logger.info(f"No push tokens found for user {user_id}")
                return False

            # Prepare messages for Expo
            messages = []
            for token in tokens:
                # Validate token format
                if not PushClient.is_exponent_push_token(token.token):
                    logger.warning(f"Invalid Expo push token: {token.token}")
                    continue

                # TODO: Fill in the missing fields
                message = PushMessage(
                    to=token.token,
                    title=title,
                    body=body,
                    data=data or {},
                    sound="default",
                    priority="high",
                    channel_id="agent-questions",
                    ttl=None,
                    expiration=None,
                    badge=None,
                    category=None,
                    display_in_foreground=True,
                    subtitle=None,
                    mutable_content=False,
                )
                messages.append(message)

            if not messages:
                logger.warning("No valid push tokens to send to")
                return False

            # Send to Expo Push API in chunks
            try:
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

                logger.info(f"Successfully sent push notifications to user {user_id}")
                return True

            except PushServerError as e:
                logger.error(f"Push server error: {str(e)}")
                return False
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
            logger.error(f"Error sending push notification: {str(e)}")
            return False

    def send_question_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        question_id: str,
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
            "questionId": question_id,
        }

        return self.send_notification(
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
