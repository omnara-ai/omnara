"""Webhook utilities for calling external webhooks when user responds."""

import asyncio
import logging
from typing import Optional
from uuid import UUID

import httpx
from sqlalchemy.orm import Session
from shared.database import Message, SenderType

logger = logging.getLogger(__name__)


def get_webhook_for_response(db: Session, instance_id: UUID) -> Optional[str]:
    """Get the webhook URL from the most recent agent message that requires user input.

    Args:
        db: Database session
        instance_id: Agent instance ID

    Returns:
        Webhook URL if found, None otherwise
    """
    # Find the most recent agent message that requires user input
    agent_message = (
        db.query(Message)
        .filter(
            Message.agent_instance_id == instance_id,
            Message.sender_type == SenderType.AGENT,
            Message.requires_user_input.is_(True),
        )
        .order_by(Message.created_at.desc())
        .first()
    )

    if not agent_message or not agent_message.message_metadata:
        return None

    # Extract webhook URL from metadata
    return agent_message.message_metadata.get("webhook_url")


async def call_webhook_async(
    webhook_url: str,
    message_id: str,
    agent_instance_id: str,
    user_response: str,
) -> bool:
    """Call a webhook URL with user response data.

    Args:
        webhook_url: The webhook URL to call
        message_id: ID of the user's response message
        agent_instance_id: Agent instance ID
        user_response: The user's response text

    Returns:
        True if webhook call succeeded, False otherwise
    """
    payload = {
        "message_id": message_id,
        "agent_instance_id": agent_instance_id,
        "user_response": user_response,
        "timestamp": asyncio.get_event_loop().time(),
    }

    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            response = await client.post(
                webhook_url,
                json=payload,
                headers={"Content-Type": "application/json"},
            )

            if response.status_code in (200, 201, 202, 204):
                logger.info(f"Successfully called webhook: {webhook_url}")
                return True
            else:
                logger.warning(
                    f"Webhook call failed with status {response.status_code}: {webhook_url}"
                )
                return False

    except httpx.TimeoutException:
        logger.error(f"Webhook call timed out: {webhook_url}")
        return False
    except Exception as e:
        logger.error(f"Error calling webhook {webhook_url}: {str(e)}")
        return False


def trigger_webhook_if_exists(
    db: Session,
    instance_id: UUID,
    message_id: UUID,
    user_response: str,
) -> None:
    """Check for and trigger webhook if one exists for this response.

    This function runs the webhook call in the background without blocking.

    Args:
        db: Database session
        instance_id: Agent instance ID
        message_id: ID of the user's response message
        user_response: The user's response text
    """
    webhook_url = get_webhook_for_response(db, instance_id)

    if webhook_url:
        logger.info(f"Found webhook URL for instance {instance_id}: {webhook_url}")

        # Create a new event loop in a thread to run the async webhook call
        # This allows us to fire-and-forget without blocking the response
        import threading

        def run_webhook():
            loop = asyncio.new_event_loop()
            asyncio.set_event_loop(loop)
            try:
                loop.run_until_complete(
                    call_webhook_async(
                        webhook_url,
                        str(message_id),
                        str(instance_id),
                        user_response,
                    )
                )
            finally:
                loop.close()

        thread = threading.Thread(target=run_webhook)
        thread.daemon = True  # Don't wait for thread to complete
        thread.start()

        logger.info(f"Started webhook call in background for instance {instance_id}")
