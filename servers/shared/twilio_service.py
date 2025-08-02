"""Twilio notification service for SMS and email notifications"""

import logging
from typing import Optional
from uuid import UUID
from sqlalchemy.orm import Session

from twilio.rest import Client
from twilio.base.exceptions import TwilioException
from sendgrid import SendGridAPIClient
from sendgrid.helpers.mail import Mail

from shared.config import settings
from shared.database import User
from .notification_base import NotificationServiceBase

logger = logging.getLogger(__name__)


class TwilioNotificationService(NotificationServiceBase):
    """Service for sending notifications via Twilio (SMS) and SendGrid (Email)"""

    def __init__(self):
        self.twilio_client = None
        self.sendgrid_client = None

        # Initialize Twilio client if credentials are provided
        if settings.twilio_account_sid and settings.twilio_auth_token:
            try:
                self.twilio_client = Client(
                    settings.twilio_account_sid, settings.twilio_auth_token
                )
                logger.info("Twilio client initialized successfully")
            except Exception as e:
                logger.error(f"Failed to initialize Twilio client: {e}")

        # Initialize SendGrid client if API key is provided
        if settings.twilio_sendgrid_api_key:
            try:
                self.sendgrid_client = SendGridAPIClient(
                    settings.twilio_sendgrid_api_key
                )
                logger.info("SendGrid client initialized successfully")
            except Exception as e:
                logger.error(f"Failed to initialize SendGrid client: {e}")

    def send_sms(
        self, to_number: str, body: str, from_number: Optional[str] = None
    ) -> bool:
        """Send SMS notification via Twilio"""
        if not self.twilio_client:
            logger.warning("Twilio client not configured, skipping SMS")
            return False

        if not to_number:
            logger.warning("No phone number provided for SMS")
            return False

        from_number = from_number or settings.twilio_from_phone_number
        if not from_number:
            logger.error("No from phone number configured")
            return False

        try:
            message = self.twilio_client.messages.create(
                body=body, from_=from_number, to=to_number
            )
            logger.info(f"SMS sent successfully: {message.sid}")
            return True
        except TwilioException as e:
            logger.error(f"Twilio error sending SMS: {e}")
            return False
        except Exception as e:
            logger.error(f"Unexpected error sending SMS: {e}")
            return False

    def send_email(
        self,
        to_email: str,
        subject: str,
        body: str,
        html_body: Optional[str] = None,
        from_email: Optional[str] = None,
    ) -> bool:
        """Send email notification via SendGrid"""
        if not self.sendgrid_client:
            logger.warning("SendGrid client not configured, skipping email")
            return False

        if not to_email:
            logger.warning("No email address provided")
            return False

        from_email = from_email or settings.twilio_from_email
        if not from_email:
            logger.error("No from email configured")
            return False

        try:
            message = Mail(
                from_email=from_email,
                to_emails=to_email,
                subject=subject,
                plain_text_content=body,
                html_content=html_body,
            )

            response = self.sendgrid_client.send(message)
            logger.info(f"Email sent successfully: {response.status_code}")
            return response.status_code in [200, 201, 202]
        except Exception as e:
            logger.error(f"Error sending email: {e}")
            return False

    def send_notification(
        self,
        db: Session,
        user_id: UUID,
        title: str,
        body: str,
        sms_body: Optional[str] = None,
        send_email: bool | None = None,
        send_sms: bool | None = None,
    ) -> dict[str, bool]:
        """Send notification via user's preferred channels"""
        results = {"sms": False, "email": False}

        try:
            # Get user preferences
            user = db.query(User).filter(User.id == user_id).first()
            if not user:
                logger.error(f"User {user_id} not found")
                return results

            # User preferences are the ultimate authority
            # Only send if BOTH the user has it enabled AND the API call requests it (or doesn't specify)
            should_send_email = user.email_notifications_enabled and (
                send_email if send_email is not None else True
            )
            should_send_sms = user.sms_notifications_enabled and (
                send_sms if send_sms is not None else True
            )

            # Log when notifications are blocked by user preferences
            if send_email and not user.email_notifications_enabled:
                logger.info(
                    f"Email notification blocked by user preferences for user {user_id}"
                )
            if send_sms and not user.sms_notifications_enabled:
                logger.info(
                    f"SMS notification blocked by user preferences for user {user_id}"
                )

            # Send SMS if enabled and phone number is available
            if should_send_sms and user.phone_number and self.twilio_client:
                sms_message = sms_body or body
                # Truncate SMS to 160 characters
                if len(sms_message) > 160:
                    sms_message = sms_message[:157] + "..."
                results["sms"] = self.send_sms(user.phone_number, sms_message)

            # Send email if enabled
            notification_email = user.notification_email or user.email
            if should_send_email and notification_email and self.sendgrid_client:
                results["email"] = self.send_email(
                    notification_email, subject=title, body=body
                )

            return results

        except Exception as e:
            logger.error(f"Error sending Twilio notifications: {e}")
            return results

    async def send_question_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        question_text: str,
        send_email: bool | None = None,
        send_sms: bool | None = None,
    ) -> dict[str, bool]:
        """Send notification for new agent question"""
        # Format agent name for display
        display_name = agent_name.replace("_", " ").title()
        title = f"{display_name} needs your input"

        # Email body with more detail
        email_body = f"""
Your agent {display_name} has a question:

{question_text}

You can respond at: https://omnara.com/dashboard/instances/{instance_id}

Best regards,
The Omnara Team
"""

        # SMS body (shorter)
        sms_body = f"{display_name}: {question_text}"

        return self.send_notification(
            db=db,
            user_id=user_id,
            title=title,
            body=email_body,
            sms_body=sms_body,
            send_email=send_email,
            send_sms=send_sms,
        )

    async def send_step_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        step_description: str,
        send_email: bool | None = None,
        send_sms: bool | None = None,
    ) -> dict[str, bool]:
        """Send notification for new agent step"""
        # Format agent name for display
        display_name = agent_name.replace("_", " ").title()
        title = f"{display_name} - New Step"

        # Email body with more detail
        email_body = f"""
Your agent {display_name} has logged a new step:

{step_description}

You can view the full session at: https://omnara.com/dashboard/instances/{instance_id}

Best regards,
The Omnara Team
"""

        # SMS body (shorter)
        sms_body = f"{display_name}: {step_description}"
        if len(sms_body) > 160:
            sms_body = f"{display_name}: {step_description[:140]}..."

        return self.send_notification(
            db=db,
            user_id=user_id,
            title=title,
            body=email_body,
            sms_body=sms_body,
            send_email=send_email,
            send_sms=send_sms,
        )


# Singleton instance
twilio_service = TwilioNotificationService()
