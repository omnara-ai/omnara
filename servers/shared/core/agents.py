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
from servers.shared.notifications import push_service

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

    # Create step
    step = log_step(db, instance.id, step_description)

    # Get unretrieved feedback
    feedback = get_and_mark_unretrieved_feedback(db, instance.id)

    return str(instance.id), step.step_number, feedback


def create_agent_question(
    db: Session,
    agent_instance_id: str,
    question_text: str,
    user_id: str,
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

    # Create question
    question = create_question(db, instance.id, question_text)

    # Send push notification
    try:
        # Get agent name from instance
        agent_name = instance.user_agent.name if instance.user_agent else "Agent"

        # Send notification
        push_service.send_question_notification(
            db=db,
            user_id=instance.user_id,
            instance_id=str(instance.id),
            question_id=str(question.id),
            agent_name=agent_name,
            question_text=question_text,
        )
    except Exception as e:
        # Log error but don't fail the question creation
        logger.error(f"Failed to send push notification: {e}")

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
