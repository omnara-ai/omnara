import asyncio
import time
import logging
from datetime import datetime, timezone
from uuid import UUID

import httpx
from shared.database import (
    AgentInstance,
    AgentStatus,
    Message,
    SenderType,
    UserAgent,
)
from shared.database.billing_operations import check_agent_limit
from shared.database.utils import sanitize_git_diff
from shared.llms import generate_conversation_title
from sqlalchemy.orm import Session
from fastmcp import Context

logger = logging.getLogger(__name__)


def create_or_get_user_agent(db: Session, name: str, user_id: str) -> UserAgent:
    """Create or get a non-deleted user agent by name for a specific user"""
    # Normalize name to lowercase for consistent storage
    normalized_name = name.lower()

    # Only look for non-deleted user agents
    user_agent = (
        db.query(UserAgent)
        .filter(
            UserAgent.name == normalized_name,
            UserAgent.user_id == UUID(user_id),
            UserAgent.is_deleted.is_(False),
        )
        .first()
    )
    if not user_agent:
        user_agent = UserAgent(
            name=normalized_name,
            user_id=UUID(user_id),
            is_active=True,
            is_deleted=False,  # Explicitly set to False for new agents
        )
        db.add(user_agent)
        db.flush()  # Flush to get the user_agent ID
    return user_agent


def create_agent_instance(
    db: Session, user_agent_id: UUID | None, user_id: str
) -> AgentInstance:
    """Create a new agent instance"""
    # Check usage limits if billing is enabled
    check_agent_limit(UUID(user_id), db)

    instance = AgentInstance(
        user_agent_id=user_agent_id, user_id=UUID(user_id), status=AgentStatus.ACTIVE
    )
    db.add(instance)
    return instance


def get_agent_instance(db: Session, instance_id: str) -> AgentInstance | None:
    """Get an agent instance by ID"""
    return db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()


def get_or_create_agent_instance(
    db: Session, agent_instance_id: str, user_id: str, agent_type: str | None = None
) -> AgentInstance:
    """Get an existing agent instance or create a new one.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID (always required)
        user_id: User ID requesting access
        agent_type: Agent type name (required only when creating new instance)

    Returns:
        The agent instance (existing or newly created)

    Raises:
        ValueError: If instance not found, user doesn't have access, or agent_type missing when creating
    """
    # Try to get existing instance
    instance = get_agent_instance(db, agent_instance_id)

    if instance:
        # Validate access to existing instance
        if str(instance.user_id) != user_id:
            raise ValueError(
                "Access denied. Agent instance does not belong to authenticated user."
            )
        return instance
    else:
        # Create new instance with the provided ID
        if not agent_type:
            raise ValueError("agent_type is required when creating new instance")

        agent_type_obj = create_or_get_user_agent(db, agent_type, user_id)

        # Check usage limits if billing is enabled
        check_agent_limit(UUID(user_id), db)

        # Create instance with the specific ID
        instance = AgentInstance(
            id=UUID(agent_instance_id),
            user_agent_id=agent_type_obj.id,
            user_id=UUID(user_id),
            status=AgentStatus.ACTIVE,
        )
        db.add(instance)
        db.flush()  # Flush to ensure the instance is in the session with its ID
        return instance


def update_session_title_if_needed(
    db: Session,
    instance_id: UUID,
    user_message: str,
) -> None:
    """
    Update the session title if it's NULL by generating a title from the user message.

    This function:
    - Checks if the instance name is NULL
    - If NULL, generates a title using the LLM
    - Updates the instance name in the database
    - Handles errors gracefully

    Args:
        db: Database session
        instance_id: Agent instance ID
        user_message: The user's message content
    """
    try:
        # Get the instance and check if name is already set
        instance = (
            db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
        )
        if not instance:
            logger.warning(f"Instance {instance_id} not found for title generation")
            return

        if instance.name is not None:
            logger.debug(
                f"Instance {instance_id} already has a name, skipping title generation"
            )
            return

        # Generate the title using the LLM utility
        title = generate_conversation_title(user_message)

        if title:
            instance.name = title
            db.commit()
            logger.info(f"Updated instance {instance_id} with title: {title}")
        else:
            logger.debug(f"No title generated for instance {instance_id}")

    except Exception as e:
        logger.error(
            f"Failed to update session title for instance {instance_id}: {str(e)}"
        )
        try:
            db.rollback()
        except Exception:
            pass


def end_session(db: Session, agent_instance_id: str, user_id: str) -> tuple[str, str]:
    """End an agent session by marking it as completed.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID to end
        user_id: Authenticated user ID

    Returns:
        Tuple of (agent_instance_id, final_status)
    """
    instance = get_or_create_agent_instance(db, agent_instance_id, user_id)

    instance.status = AgentStatus.COMPLETED
    instance.ended_at = datetime.now(timezone.utc)
    instance.last_heartbeat_at = datetime.now(timezone.utc)

    return str(instance.id), instance.status.value


def create_agent_message(
    db: Session,
    instance_id: UUID,
    content: str,
    requires_user_input: bool = False,
    message_metadata: dict | None = None,
) -> Message:
    """Create a new agent message without committing"""
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
    if instance and instance.status != AgentStatus.COMPLETED:
        if requires_user_input:
            instance.status = AgentStatus.AWAITING_INPUT
        else:
            instance.status = AgentStatus.ACTIVE
        # Stamp heartbeat for any agent activity
        instance.last_heartbeat_at = datetime.now(timezone.utc)

    message = Message(
        agent_instance_id=instance_id,
        sender_type=SenderType.AGENT,
        content=content,
        requires_user_input=requires_user_input,
        message_metadata=message_metadata,
    )
    db.add(message)
    db.flush()  # Flush to get the message ID

    # Update last read message
    if instance:
        instance.last_read_message_id = message.id

    return message


async def wait_for_answer(
    db: Session,
    question_id: UUID,
    timeout_seconds: int = 86400,  # 24 hours default
    tool_context: Context | None = None,
) -> str | None:
    """Wait for an answer to a question using polling"""
    start_time = time.time()
    last_progress_report = start_time
    total_minutes = timeout_seconds // 60

    # Get the question message
    question = db.query(Message).filter(Message.id == question_id).first()
    if not question or not question.requires_user_input:
        return None

    while time.time() - start_time < timeout_seconds:
        # Check if agent has moved on (last read message changed)
        instance = (
            db.query(AgentInstance)
            .filter(AgentInstance.id == question.agent_instance_id)
            .first()
        )

        # If last_read_message_id has changed from our question, agent has moved on
        if instance and instance.last_read_message_id != question_id:
            return None

        # Check for a user message after this question
        answer = (
            db.query(Message)
            .filter(
                Message.agent_instance_id == question.agent_instance_id,
                Message.sender_type == SenderType.USER,
                Message.created_at > question.created_at,
            )
            .order_by(Message.created_at)
            .first()
        )

        if answer:
            # Update last read message to this answer
            if instance:
                instance.last_read_message_id = answer.id

            if tool_context:
                await tool_context.report_progress(total_minutes, total_minutes)

            return answer.content

        # Report progress every minute if tool_context is provided
        current_time = time.time()
        if tool_context and (current_time - last_progress_report) >= 60:
            elapsed_minutes = int((current_time - start_time) / 60)
            await tool_context.report_progress(elapsed_minutes, total_minutes)
            last_progress_report = current_time

        await asyncio.sleep(1)

    return None


def get_queued_user_messages(
    db: Session, instance_id: UUID, last_read_message_id: UUID | None = None
) -> list[Message] | None:
    """Get all user messages since the agent last read them.

    Args:
        db: Database session
        instance_id: Agent instance ID
        last_read_message_id: The message ID the agent last read (optional)

    Returns:
        - None if last_read_message_id doesn't match the instance's current last_read_message_id
        - Empty list if no new messages
        - List of messages if there are new user messages
    """
    # Get the agent instance to check last read message
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
    if not instance:
        return []

    if (
        last_read_message_id is not None
        and instance.last_read_message_id != last_read_message_id
    ):
        return None

    # If no last read message, get all user messages
    if not instance.last_read_message_id:
        messages = (
            db.query(Message)
            .filter(
                Message.agent_instance_id == instance_id,
                Message.sender_type == SenderType.USER,
            )
            .order_by(Message.created_at)
            .all()
        )
    else:
        last_read_message = (
            db.query(Message)
            .filter(Message.id == instance.last_read_message_id)
            .first()
        )

        if not last_read_message:
            return []

        # Get all user messages after the last read message
        messages = (
            db.query(Message)
            .filter(
                Message.agent_instance_id == instance_id,
                Message.sender_type == SenderType.USER,
                Message.created_at > last_read_message.created_at,
            )
            .order_by(Message.created_at)
            .all()
        )

    if messages and last_read_message_id is not None:
        instance.last_read_message_id = messages[-1].id

    return messages


async def send_agent_message(
    db: Session,
    agent_instance_id: str,
    content: str,
    user_id: str,
    agent_type: str | None = None,
    requires_user_input: bool = False,
    git_diff: str | None = None,
    message_metadata: dict | None = None,
) -> tuple[str, str, list[Message]]:
    """High-level function to send an agent message and get queued user messages.

    This combines the common pattern of:
    1. Getting or creating an agent instance
    2. Validating access (if existing instance)
    3. Creating a message
    4. Updating git diff if provided
    5. Getting any queued user messages

    Args:
        db: Database session
        agent_instance_id: Agent instance ID (pass None to create new)
        content: Message content
        user_id: Authenticated user ID
        agent_type: Type of agent (required if creating new instance)
        requires_user_input: Whether this is a question requiring response
        git_diff: Optional git diff to update on the instance

    Returns:
        Tuple of (agent_instance_id, message_id, list of queued user message contents)
    """
    # Get or create instance using the unified function
    instance = get_or_create_agent_instance(db, agent_instance_id, user_id, agent_type)

    # Update git diff if provided (but don't commit yet)
    if git_diff is not None:
        sanitized_diff = sanitize_git_diff(git_diff)
        if sanitized_diff is not None:  # Allow empty string (cleared diff)
            instance.git_diff = sanitized_diff
        else:
            logger.warning(
                f"Invalid git diff format for instance {instance.id}, skipping git diff update"
            )

    queued_messages = get_queued_user_messages(db, instance.id, None)

    # Create the message (this will update last_read_message_id)
    message = create_agent_message(
        db=db,
        instance_id=instance.id,
        content=content,
        requires_user_input=requires_user_input,
        message_metadata=message_metadata,
    )

    # Handle the None case (shouldn't happen here since we just created the message)
    if queued_messages is None:
        queued_messages = []

    return str(instance.id), str(message.id), queued_messages


def create_user_message(
    db: Session,
    agent_instance_id: str,
    content: str,
    user_id: str,
    mark_as_read: bool = True,
) -> dict:
    """Create a user message for an agent instance.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID to send the message to
        content: Message content
        user_id: Authenticated user ID
        mark_as_read: Whether to update last_read_message_id (default: True)

    Returns:
        Dictionary with message details:
        - id: Message ID
        - content: Message content
        - sender_type: "user"
        - created_at: Creation timestamp
        - requires_user_input: False
        - marked_as_read: Whether the message was marked as read
        - instance_id: The agent instance ID

    Raises:
        ValueError: If instance not found or user doesn't have access
    """
    instance = get_agent_instance(db, agent_instance_id)
    if not instance:
        raise ValueError("Agent instance not found")

    if str(instance.user_id) != user_id:
        raise ValueError("Agent instance not found")

    # Create the user message
    message = Message(
        agent_instance_id=UUID(agent_instance_id),
        sender_type=SenderType.USER,
        content=content,
        requires_user_input=False,
    )
    db.add(message)
    db.flush()  # Get the message ID
    db.refresh(message)  # Get database-computed values like created_at

    instance.status = AgentStatus.ACTIVE

    # Update last_read_message_id if requested
    if mark_as_read:
        instance.last_read_message_id = message.id

    # Trigger webhook if previous agent message was waiting for response
    # TODO: do this in a background task
    trigger_webhook_for_user_response(
        db=db,
        agent_instance_id=agent_instance_id,
        user_message_content=content,
        user_message_id=str(message.id),
        user_id=user_id,
    )

    return {
        "id": str(message.id),
        "content": message.content,
        "sender_type": message.sender_type.value,
        "created_at": message.created_at,
        "requires_user_input": message.requires_user_input,
        "marked_as_read": mark_as_read,
        "instance_id": agent_instance_id,
    }


def trigger_webhook_for_user_response(
    db: Session,
    agent_instance_id: UUID | str,
    user_message_content: str,
    user_message_id: str,
    user_id: str,
) -> None:
    """Trigger webhook if the last agent message was waiting for user input.

    This function checks if the previous agent message has a webhook URL in its
    metadata and triggers it with the user's response.
    """
    # Convert to UUID if string
    if isinstance(agent_instance_id, str):
        agent_instance_id = UUID(agent_instance_id)

    # Find the last agent message that requires user input
    last_agent_message = (
        db.query(Message)
        .filter(
            Message.agent_instance_id == agent_instance_id,
            Message.sender_type == SenderType.AGENT,
            Message.requires_user_input,
        )
        .order_by(Message.created_at.desc())
        .first()
    )

    if not last_agent_message:
        return

    # Check if it has a webhook URL in metadata
    if not last_agent_message.message_metadata:
        return

    webhook_url = last_agent_message.message_metadata.get("webhook_url")
    if not webhook_url:
        return

    # Check if webhook was already triggered
    if last_agent_message.message_metadata.get("webhook_triggered"):
        logger.info(f"Webhook already triggered for message {last_agent_message.id}")
        return

    # Prepare webhook payload
    webhook_payload = {
        "user_message": user_message_content,
        "user_id": user_id,
        "message_id": user_message_id,
        "agent_instance_id": str(agent_instance_id),
        "timestamp": datetime.now(timezone.utc).isoformat(),
    }

    # TODO: call this in the background so user message doesn't hang
    try:
        with httpx.Client() as client:
            response = client.post(
                webhook_url,
                json=webhook_payload,
                timeout=10.0,  # 10 second timeout
                headers={
                    "Content-Type": "application/json",
                    "X-Omnara-Webhook": "true",
                },
            )

            if response.status_code >= 200 and response.status_code < 300:
                logger.info(
                    f"Successfully triggered webhook for agent instance {agent_instance_id}"
                )
                # Mark webhook as triggered to prevent multiple triggers
                if not last_agent_message.message_metadata:
                    last_agent_message.message_metadata = {}
                last_agent_message.message_metadata["webhook_triggered"] = True
                last_agent_message.message_metadata["webhook_response_status"] = (
                    response.status_code
                )
            else:
                logger.warning(
                    f"Webhook returned non-success status {response.status_code} "
                    f"for agent instance {agent_instance_id}"
                )
    except Exception as e:
        logger.error(
            f"Failed to trigger webhook for agent instance {agent_instance_id}: {e}"
        )
