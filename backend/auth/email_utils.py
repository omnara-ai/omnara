import logging
from typing import Optional

from sendgrid import SendGridAPIClient
from sendgrid.helpers.mail import Mail

from shared.config.settings import settings

logger = logging.getLogger(__name__)


class EmailService:
    """Service for sending emails via SendGrid"""

    def __init__(self):
        self.sendgrid_api_key = settings.twilio_sendgrid_api_key
        self.from_email = settings.twilio_from_email or "ishaan@omnara.com"
        self.client = None

        if self.sendgrid_api_key:
            self.client = SendGridAPIClient(self.sendgrid_api_key)
        else:
            logger.warning("SendGrid API key not configured")

    def send_welcome_email(
        self, to_email: str, display_name: Optional[str] = None
    ) -> bool:
        """Send welcome email to new user"""
        if not self.client:
            logger.error("SendGrid client not initialized")
            return False

        try:
            subject = "🚀 Welcome to Omnara — let's launch your first agent"

            # Extract first name from display name or use default
            first_name = "there"
            if display_name:
                first_name = display_name.split()[0]

            # Welcome email content
            html_content = f"""
            <html>
                <body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px; color: #333; line-height: 1.6;">
                    <p>Hi {first_name},</p>

                    <p>Thanks for installing Omnara.<br>
                    Here's the fastest way to see it in action:</p>

                    <div style="background-color: #f6f8fa; padding: 16px; border-radius: 6px; margin: 20px 0; font-family: 'SF Mono', Consolas, 'Liberation Mono', Menlo, monospace; font-size: 14px;">
                        <code style="color: #e83e8c;">pip install omnara</code> <code style="color: #333;">&&</code> <code style="color: #e83e8c;">omnara</code>
                    </div>

                    <p>Then open your dashboard:<br>
                    👉 <a href="https://omnara.com/dashboard" style="color: #0066cc; text-decoration: none;">https://omnara.com/dashboard</a></p>

                    <p>You'll see your agent running live — and get a push notification if it needs you.</p>

                    <p>If something breaks, reply to this email — it comes straight to me.</p>

                    <p>– Ishaan<br>
                    Co-founder, Omnara</p>
                </body>
            </html>
            """

            message = Mail(
                from_email=self.from_email,
                to_emails=to_email,
                subject=subject,
                html_content=html_content,
            )

            response = self.client.send(message)

            if response.status_code >= 200 and response.status_code < 300:
                logger.info(f"Welcome email sent successfully to {to_email}")
                return True
            else:
                logger.error(
                    f"Failed to send welcome email to {to_email}: {response.status_code}"
                )
                return False

        except Exception as e:
            logger.error(f"Error sending welcome email to {to_email}: {str(e)}")
            return False


# Singleton instance
email_service = EmailService()
