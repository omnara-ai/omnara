"""Shared business logic for agent operations.

This module contains the common logic used by both MCP and FastAPI servers,
avoiding code duplication while allowing protocol-specific implementations.
"""

import logging
from sqlalchemy.orm import Session

from servers.shared.db import (
    get_agent_instance,
    create_agent_instance,
    log_step,
    create_question,
    get_and_mark_unretrieved_feedback,
    create_or_get_user_agent,
    end_session,
)
from shared.database.utils import sanitize_git_diff

logger = logging.getLogger(__name__)


def validate_agent_access(db: Session, agent_instance_id: str, user_id: str):
    """Validate that a user has access to an agent instance.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID to validate
        user_id: User ID requesting access

    Returns:
        The agent instance if validation passes

    Raises:
        ValueError: If instance not found or user doesn't have access
    """
    instance = get_agent_instance(db, agent_instance_id)
    if not instance:
        raise ValueError(f"Agent instance {agent_instance_id} not found")
    if str(instance.user_id) != user_id:
        raise ValueError(
            "Access denied. Agent instance does not belong to authenticated user."
        )
    return instance


def process_log_step(
    db: Session,
    agent_type: str,
    step_description: str,
    user_id: str,
    agent_instance_id: str | None = None,
    send_email: bool | None = None,
    send_sms: bool | None = None,
    send_push: bool | None = None,
    git_diff: str | None = None,
) -> tuple[str, int, list[str]]:
    """Process a log step operation with all common logic.

    Args:
        db: Database session
        agent_type: Type of agent
        step_description: Description of the step
        user_id: Authenticated user ID
        agent_instance_id: Optional existing instance ID

    Returns:
        Tuple of (agent_instance_id, step_number, user_feedback)
    """
    # Get or create user agent type
    agent_type_obj = create_or_get_user_agent(db, agent_type, user_id)

    # Get or create instance
    if agent_instance_id:
        instance = validate_agent_access(db, agent_instance_id, user_id)
    else:
        instance = create_agent_instance(db, agent_type_obj.id, user_id)

    # Create step with notification preferences
    step = log_step(db, instance.id, step_description, send_email, send_sms, send_push)

    # Update git diff if provided
    if git_diff is not None:
        # Validate and sanitize git diff
        sanitized_diff = sanitize_git_diff(git_diff)
        if sanitized_diff is not None:  # Allow empty string (cleared diff)
            instance.git_diff = sanitized_diff
            db.commit()
        else:
            logger.warning(
                f"Invalid git diff format for instance {instance.id}, skipping git diff update"
            )

    # Get unretrieved feedback
    feedback = get_and_mark_unretrieved_feedback(db, instance.id)

    return str(instance.id), step.step_number, feedback


async def create_agent_question(
    db: Session,
    agent_instance_id: str,
    question_text: str,
    user_id: str,
    send_email: bool | None = None,
    send_sms: bool | None = None,
    send_push: bool | None = None,
    git_diff: str | None = None,
):
    """Create a question with validation and send push notification.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID
        question_text: Question to ask
        user_id: Authenticated user ID

    Returns:
        The created question object
    """
    # Validate access
    instance = validate_agent_access(db, agent_instance_id, user_id)

    # Update git diff if provided
    if git_diff is not None:
        # Validate and sanitize git diff
        sanitized_diff = sanitize_git_diff(git_diff)
        if sanitized_diff is not None:  # Allow empty string (cleared diff)
            instance.git_diff = sanitized_diff
            db.commit()
        else:
            logger.warning(
                f"Invalid git diff format for instance {instance.id}, skipping git diff update"
            )

    # Create question
    # Note: Notifications sent by create_question() function based on parameters
    question = await create_question(
        db, instance.id, question_text, send_email, send_sms, send_push
    )

    return question


def process_end_session(
    db: Session,
    agent_instance_id: str,
    user_id: str,
) -> tuple[str, str]:
    """Process ending a session with validation.

    Args:
        db: Database session
        agent_instance_id: Agent instance ID to end
        user_id: Authenticated user ID

    Returns:
        Tuple of (agent_instance_id, final_status)
    """
    # Validate access
    instance = validate_agent_access(db, agent_instance_id, user_id)

    # End the session
    updated_instance = end_session(db, instance.id)

    return str(updated_instance.id), updated_instance.status.value
