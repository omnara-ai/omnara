import asyncio
import time
import logging
from datetime import datetime, timezone
from uuid import UUID

from shared.database import (
    AgentInstance,
    AgentQuestion,
    AgentStatus,
    AgentStep,
    AgentUserFeedback,
    UserAgent,
)
from sqlalchemy import func
from sqlalchemy.orm import Session
from fastmcp import Context

logger = logging.getLogger(__name__)


def create_or_get_user_agent(db: Session, name: str, user_id: str) -> UserAgent:
    """Create or get a user agent by name for a specific user"""
    # Normalize name to lowercase for consistent storage
    normalized_name = name.lower()

    user_agent = (
        db.query(UserAgent)
        .filter(UserAgent.name == normalized_name, UserAgent.user_id == UUID(user_id))
        .first()
    )
    if not user_agent:
        user_agent = UserAgent(
            name=normalized_name,
            user_id=UUID(user_id),
            is_active=True,
        )
        db.add(user_agent)
        db.commit()
        db.refresh(user_agent)
    return user_agent


def create_agent_instance(
    db: Session, user_agent_id: UUID | None, user_id: str
) -> AgentInstance:
    """Create a new agent instance"""
    instance = AgentInstance(
        user_agent_id=user_agent_id, user_id=UUID(user_id), status=AgentStatus.ACTIVE
    )
    db.add(instance)
    db.commit()
    db.refresh(instance)
    return instance


def get_agent_instance(db: Session, instance_id: str) -> AgentInstance | None:
    """Get an agent instance by ID"""
    return db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()


def log_step(db: Session, instance_id: UUID, description: str) -> AgentStep:
    """Log a new step for an agent instance"""
    # Get the next step number
    max_step = (
        db.query(func.max(AgentStep.step_number))
        .filter(AgentStep.agent_instance_id == instance_id)
        .scalar()
    )
    next_step_number = (max_step or 0) + 1

    # Create the step
    step = AgentStep(
        agent_instance_id=instance_id,
        step_number=next_step_number,
        description=description,
    )
    db.add(step)
    db.commit()
    db.refresh(step)
    return step


def create_question(
    db: Session, instance_id: UUID, question_text: str
) -> AgentQuestion:
    """Create a new question for an agent instance"""
    # Mark any existing active questions as inactive
    db.query(AgentQuestion).filter(
        AgentQuestion.agent_instance_id == instance_id, AgentQuestion.is_active
    ).update({"is_active": False})

    # Update agent instance status to awaiting_input
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
    if instance and instance.status == AgentStatus.ACTIVE:
        instance.status = AgentStatus.AWAITING_INPUT

    # Create new question
    question = AgentQuestion(
        agent_instance_id=instance_id, question_text=question_text, is_active=True
    )
    db.add(question)
    db.commit()
    db.refresh(question)

    # Send push notification
    try:
        from servers.shared.notifications import push_service

        # Get agent name from instance
        if instance:
            agent_name = instance.user_agent.name if instance.user_agent else "Agent"

            push_service.send_question_notification(
                db=db,
                user_id=instance.user_id,
                instance_id=str(instance.id),
                question_id=str(question.id),
                agent_name=agent_name,
                question_text=question_text,
            )
    except Exception as e:
        # Don't fail the question creation if push notification fails
        logger.error(
            f"Failed to send push notification for question {question.id}: {e}"
        )

    return question


async def wait_for_answer(
    db: Session,
    question_id: UUID,
    timeout: int = 86400,
    tool_context: Context | None = None,
) -> str | None:
    """
    Wait for an answer to a question (async non-blocking)

    Args:
        db: Database session
        question_id: Question ID to wait for
        timeout: Maximum time to wait in seconds (default 24 hours)

    Returns:
        Answer text if received, None if timeout
    """
    start_time = time.time()
    last_progress_report = start_time
    total_minutes = int(timeout / 60)

    # Report initial progress (0 minutes elapsed)
    if tool_context:
        await tool_context.report_progress(0, total_minutes)

    while time.time() - start_time < timeout:
        # Check for answer
        db.commit()  # Ensure we see latest data
        question = (
            db.query(AgentQuestion).filter(AgentQuestion.id == question_id).first()
        )

        if question and question.answer_text is not None:
            if tool_context:
                await tool_context.report_progress(total_minutes, total_minutes)
            return question.answer_text

        # Report progress every minute if tool_context is provided
        current_time = time.time()
        if tool_context and (current_time - last_progress_report) >= 60:
            elapsed_minutes = int((current_time - start_time) / 60)
            await tool_context.report_progress(elapsed_minutes, total_minutes)
            last_progress_report = current_time

        await asyncio.sleep(1)

    # Timeout - mark question as inactive
    db.query(AgentQuestion).filter(AgentQuestion.id == question_id).update(
        {"is_active": False}
    )
    db.commit()

    return None


def get_question(db: Session, question_id: str) -> AgentQuestion | None:
    """Get a question by ID"""
    return db.query(AgentQuestion).filter(AgentQuestion.id == question_id).first()


def get_and_mark_unretrieved_feedback(
    db: Session, instance_id: UUID, since_time: datetime | None = None
) -> list[str]:
    """Get unretrieved user feedback for an agent instance and mark as retrieved"""

    query = db.query(AgentUserFeedback).filter(
        AgentUserFeedback.agent_instance_id == instance_id,
        AgentUserFeedback.retrieved_at.is_(None),
    )

    if since_time:
        query = query.filter(AgentUserFeedback.created_at > since_time)

    feedback_list = query.order_by(AgentUserFeedback.created_at).all()

    # Mark all feedback as retrieved
    for feedback in feedback_list:
        feedback.retrieved_at = datetime.now(timezone.utc)
    db.commit()

    return [feedback.feedback_text for feedback in feedback_list]


def end_session(db: Session, instance_id: UUID) -> AgentInstance:
    """End an agent session by marking it as completed"""
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()

    if not instance:
        raise ValueError(f"Agent instance {instance_id} not found")

    # Update status to completed
    instance.status = AgentStatus.COMPLETED
    instance.ended_at = datetime.now(timezone.utc)

    # Mark any active questions as inactive
    db.query(AgentQuestion).filter(
        AgentQuestion.agent_instance_id == instance_id, AgentQuestion.is_active
    ).update({"is_active": False})

    db.commit()
    db.refresh(instance)
    return instance
