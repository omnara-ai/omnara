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
    User,
)
from shared.database.billing_operations import check_agent_limit
from sqlalchemy import func
from sqlalchemy.orm import Session
from fastmcp import Context
from servers.shared.notifications import push_service
from servers.shared.twilio_service import twilio_service

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
    # Check usage limits if billing is enabled
    check_agent_limit(UUID(user_id), db)

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


def log_step(
    db: Session,
    instance_id: UUID,
    description: str,
    send_email: bool | None = None,
    send_sms: bool | None = None,
    send_push: bool | None = None,
) -> AgentStep:
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

    # Send notifications if requested (all default to False for log steps)
    if send_email or send_sms or send_push:
        # Get instance details for notifications
        instance = (
            db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
        )
        if instance:
            user = db.query(User).filter(User.id == instance.user_id).first()

            if user:
                agent_name = (
                    instance.user_agent.name if instance.user_agent else "Agent"
                )

                # Override defaults - for log steps, all notifications default to False
                should_send_push = send_push if send_push is not None else False
                should_send_email = send_email if send_email is not None else False
                should_send_sms = send_sms if send_sms is not None else False

                # Send push notification if explicitly enabled
                if should_send_push:
                    try:
                        asyncio.create_task(
                            push_service.send_step_notification(
                                db=db,
                                user_id=instance.user_id,
                                instance_id=str(instance.id),
                                step_number=step.step_number,
                                agent_name=agent_name,
                                step_description=description,
                            )
                        )
                    except Exception as e:
                        logger.error(
                            f"Failed to send push notification for step {step.id}: {e}"
                        )

                # Send Twilio notifications if explicitly enabled
                if should_send_email or should_send_sms:
                    try:
                        asyncio.create_task(
                            twilio_service.send_step_notification(
                                db=db,
                                user_id=instance.user_id,
                                instance_id=str(instance.id),
                                step_number=step.step_number,
                                agent_name=agent_name,
                                step_description=description,
                                send_email=should_send_email,
                                send_sms=should_send_sms,
                            )
                        )
                    except Exception as e:
                        logger.error(
                            f"Failed to send Twilio notification for step {step.id}: {e}"
                        )

    return step


async def create_question(
    db: Session,
    instance_id: UUID,
    question_text: str,
    send_email: bool | None = None,
    send_sms: bool | None = None,
    send_push: bool | None = None,
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

    # Send notifications based on user preferences
    if instance:
        # Get user for checking preferences
        user = db.query(User).filter(User.id == instance.user_id).first()

        if user:
            agent_name = instance.user_agent.name if instance.user_agent else "Agent"

            # Determine notification preferences
            # For questions: push defaults to True (or user preference), email/SMS default to False
            should_send_push = (
                send_push if send_push is not None else user.push_notifications_enabled
            )
            should_send_email = (
                send_email
                if send_email is not None
                else user.email_notifications_enabled
            )
            should_send_sms = (
                send_sms if send_sms is not None else user.sms_notifications_enabled
            )

            # Send push notification if enabled
            if should_send_push:
                try:
                    await push_service.send_question_notification(
                        db=db,
                        user_id=instance.user_id,
                        instance_id=str(instance.id),
                        question_id=str(question.id),
                        agent_name=agent_name,
                        question_text=question_text,
                    )
                except Exception as e:
                    logger.error(
                        f"Failed to send push notification for question {question.id}: {e}"
                    )

            # Send Twilio notification if enabled (email and/or SMS)
            if should_send_email or should_send_sms:
                try:
                    twilio_service.send_question_notification(
                        db=db,
                        user_id=instance.user_id,
                        instance_id=str(instance.id),
                        question_id=str(question.id),
                        agent_name=agent_name,
                        question_text=question_text,
                        send_email=should_send_email,
                        send_sms=should_send_sms,
                    )
                except Exception as e:
                    logger.error(
                        f"Failed to send Twilio notification for question {question.id}: {e}"
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
