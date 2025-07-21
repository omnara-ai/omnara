import logging
from datetime import datetime, timezone
from uuid import UUID

from shared.config import settings
from shared.database import (
    AgentInstance,
    AgentQuestion,
    AgentStatus,
    AgentStep,
    AgentUserFeedback,
    APIKey,
    PushToken,
    User,
    UserAgent,
)
from shared.database.billing_operations import get_or_create_subscription
from shared.database.subscription_models import BillingEvent, Subscription
from sqlalchemy import desc, func
from sqlalchemy.orm import Session, joinedload


def _format_instance(instance: AgentInstance) -> dict:
    """Helper function to format an agent instance consistently"""
    # Get latest step
    latest_step = None
    if instance.steps:
        latest_step = max(instance.steps, key=lambda s: s.created_at).description

    # Get step count
    step_count = len(instance.steps) if instance.steps else 0

    # Check for pending questions
    pending_questions = [q for q in instance.questions if q.is_active]
    pending_questions_count = len(pending_questions)
    has_pending = pending_questions_count > 0
    pending_age = None
    if has_pending:
        oldest_pending = min(pending_questions, key=lambda q: q.asked_at)
        # All database times are stored as UTC but may be naive
        now_utc = datetime.now(timezone.utc)
        asked_at = oldest_pending.asked_at
        if asked_at.tzinfo is None:
            asked_at = asked_at.replace(tzinfo=timezone.utc)
        pending_age = int((now_utc - asked_at).total_seconds())

    return {
        "id": str(instance.id),
        "agent_type_id": str(instance.user_agent_id) if instance.user_agent_id else "",
        "agent_type_name": instance.user_agent.name
        if instance.user_agent
        else "Unknown",
        "status": instance.status,
        "started_at": instance.started_at,
        "ended_at": instance.ended_at,
        "latest_step": latest_step,
        "has_pending_question": has_pending,
        "pending_question_age": pending_age,
        "pending_questions_count": pending_questions_count,
        "step_count": step_count,
        "last_signal_at": instance.steps[-1].created_at
        if instance.steps
        else instance.started_at,
    }


def get_all_agent_types_with_instances(db: Session, user_id: UUID) -> list[dict]:
    """Get all user agents with their instances for a specific user"""

    # Get all user agents for this user
    user_agents = db.query(UserAgent).filter(UserAgent.user_id == user_id).all()

    result = []
    for user_agent in user_agents:
        # Get all instances for this user agent
        instances = (
            db.query(AgentInstance)
            .filter(
                AgentInstance.user_agent_id == user_agent.id,
            )
            .options(
                joinedload(AgentInstance.steps),
                joinedload(AgentInstance.questions),
                joinedload(AgentInstance.user_agent),
            )
            .all()
        )

        # Sort instances: pending questions first, then by most recent activity
        def sort_key(instance):
            pending_questions = [q for q in instance.questions if q.is_active]
            if pending_questions:
                oldest_question = min(pending_questions, key=lambda q: q.asked_at)
                return (0, oldest_question.asked_at)

            last_activity = instance.started_at
            if instance.steps:
                last_activity = max(
                    instance.steps, key=lambda s: s.created_at
                ).created_at
            return (1, -last_activity.timestamp())

        sorted_instances = sorted(instances, key=sort_key)

        # Format instances with helper function
        formatted_instances = [
            _format_instance(instance) for instance in sorted_instances
        ]

        result.append(
            {
                "id": str(user_agent.id),
                "name": user_agent.name,
                "created_at": user_agent.created_at,
                "recent_instances": formatted_instances,
                "total_instances": len(instances),
                "active_instances": sum(
                    1 for i in instances if i.status == AgentStatus.ACTIVE
                ),
            }
        )

    return result


def get_all_agent_instances(
    db: Session, user_id: UUID, limit: int | None = None
) -> list[dict]:
    """Get all agent instances for a specific user, sorted by most recent activity"""

    query = (
        db.query(AgentInstance)
        .filter(AgentInstance.user_id == user_id)
        .options(
            joinedload(AgentInstance.steps),
            joinedload(AgentInstance.questions),
            joinedload(AgentInstance.user_agent),
        )
        .order_by(desc(AgentInstance.started_at))
    )

    if limit is not None:
        query = query.limit(limit)

    instances = query.all()

    # Format instances using helper function
    return [_format_instance(instance) for instance in instances]


def get_agent_summary(db: Session, user_id: UUID) -> dict:
    """Get lightweight summary of agent counts without fetching detailed instance data"""

    # Count total instances
    total_instances = (
        db.query(AgentInstance).filter(AgentInstance.user_id == user_id).count()
    )

    # Count active instances (only 'active' for now until DB enum is updated)
    active_instances = (
        db.query(AgentInstance)
        .filter(
            AgentInstance.user_id == user_id, AgentInstance.status == AgentStatus.ACTIVE
        )
        .count()
    )

    # Count completed instances
    completed_instances = (
        db.query(AgentInstance)
        .filter(
            AgentInstance.user_id == user_id,
            AgentInstance.status == AgentStatus.COMPLETED,
        )
        .count()
    )

    # Count by user agent and status (for fleet overview)
    # Get instances with their user agents
    agent_type_stats = (
        db.query(
            UserAgent.id,
            UserAgent.name,
            AgentInstance.status,
            func.count(AgentInstance.id).label("count"),
        )
        .join(AgentInstance, AgentInstance.user_agent_id == UserAgent.id)
        .filter(UserAgent.user_id == user_id)
        .group_by(UserAgent.id, UserAgent.name, AgentInstance.status)
        .all()
    )

    # Format agent type stats
    agent_types_summary = {}
    for type_id, type_name, status, count in agent_type_stats:
        # Agent types are now stored in lowercase, so no normalization needed
        if type_name not in agent_types_summary:
            agent_types_summary[type_name] = {
                "id": str(type_id),
                "name": type_name,
                "total_instances": 0,
                "active_instances": 0,
            }

        agent_types_summary[type_name]["total_instances"] += count
        if status == AgentStatus.ACTIVE:
            agent_types_summary[type_name]["active_instances"] += count

    return {
        "total_instances": total_instances,
        "active_instances": active_instances,
        "completed_instances": completed_instances,
        "agent_types": list(agent_types_summary.values()),
    }


def get_agent_type_instances(
    db: Session, agent_type_id: UUID, user_id: UUID
) -> list[dict] | None:
    """Get all instances for a specific user agent"""

    user_agent = (
        db.query(UserAgent)
        .filter(UserAgent.id == agent_type_id, UserAgent.user_id == user_id)
        .first()
    )
    if not user_agent:
        return None

    instances = (
        db.query(AgentInstance)
        .filter(
            AgentInstance.user_agent_id == agent_type_id,
        )
        .options(
            joinedload(AgentInstance.steps),
            joinedload(AgentInstance.questions),
            joinedload(AgentInstance.user_agent),
        )
        .order_by(desc(AgentInstance.started_at))
        .all()
    )

    # Format instances using helper function
    return [_format_instance(instance) for instance in instances]


def get_agent_instance_detail(
    db: Session, instance_id: UUID, user_id: UUID
) -> dict | None:
    """Get detailed information about a specific agent instance for a specific user"""

    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .options(
            joinedload(AgentInstance.user_agent),
            joinedload(AgentInstance.steps),
            joinedload(AgentInstance.questions),
            joinedload(AgentInstance.user_feedback),
        )
        .first()
    )

    if not instance:
        return None

    # Sort steps by step number
    sorted_steps = sorted(instance.steps, key=lambda s: s.step_number)

    # Sort questions by asked_at
    sorted_questions = sorted(instance.questions, key=lambda q: q.asked_at)

    # Sort user feedback by created_at
    sorted_feedback = sorted(instance.user_feedback, key=lambda f: f.created_at)

    return {
        "id": str(instance.id),
        "agent_type_id": str(instance.user_agent_id) if instance.user_agent_id else "",
        "agent_type": {
            "id": str(instance.user_agent.id) if instance.user_agent else "",
            "name": instance.user_agent.name if instance.user_agent else "Unknown",
            "created_at": instance.user_agent.created_at
            if instance.user_agent
            else datetime.now(timezone.utc),
            "recent_instances": [],
            "total_instances": 0,
            "active_instances": 0,
        },
        "status": instance.status,
        "started_at": instance.started_at,
        "ended_at": instance.ended_at,
        "git_diff": instance.git_diff,
        "steps": [
            {
                "id": str(step.id),
                "step_number": step.step_number,
                "description": step.description,
                "created_at": step.created_at,
            }
            for step in sorted_steps
        ],
        "questions": [
            {
                "id": str(question.id),
                "question_text": question.question_text,
                "answer_text": question.answer_text,
                "asked_at": question.asked_at,
                "answered_at": question.answered_at,
                "is_active": question.is_active,
            }
            for question in sorted_questions
        ],
        "user_feedback": [
            {
                "id": str(feedback.id),
                "feedback_text": feedback.feedback_text,
                "created_at": feedback.created_at,
                "retrieved_at": feedback.retrieved_at,
            }
            for feedback in sorted_feedback
        ],
    }


def submit_answer(
    db: Session, question_id: UUID, answer: str, user_id: UUID
) -> dict | None:
    """Submit an answer to a question for a specific user"""

    question = (
        db.query(AgentQuestion)
        .filter(AgentQuestion.id == question_id, AgentQuestion.is_active)
        .join(AgentInstance)
        .filter(AgentInstance.user_id == user_id)
        .first()
    )

    if not question:
        return None

    question.answer_text = answer
    question.answered_at = datetime.now(timezone.utc)
    question.is_active = False
    question.answered_by_user_id = user_id

    # Update agent instance status back to ACTIVE if it was AWAITING_INPUT
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == question.agent_instance_id)
        .first()
    )
    if instance and instance.status == AgentStatus.AWAITING_INPUT:
        # Check if there are other active questions for this instance
        other_active_questions = (
            db.query(AgentQuestion)
            .filter(
                AgentQuestion.agent_instance_id == instance.id,
                AgentQuestion.id != question_id,
                AgentQuestion.is_active,
            )
            .count()
        )
        # Only change status back to ACTIVE if no other questions are pending
        if other_active_questions == 0:
            instance.status = AgentStatus.ACTIVE

    db.commit()

    return {
        "id": str(question.id),
        "question_text": question.question_text,
        "answer_text": question.answer_text,
        "asked_at": question.asked_at,
        "answered_at": question.answered_at,
        "is_active": question.is_active,
    }


def submit_user_feedback(
    db: Session, instance_id: UUID, feedback_text: str, user_id: UUID
) -> dict | None:
    """Submit user feedback for an agent instance for a specific user"""

    # Check if instance exists and belongs to user
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )
    if not instance:
        return None

    # Create new feedback
    feedback = AgentUserFeedback(
        agent_instance_id=instance_id,
        feedback_text=feedback_text,
        created_by_user_id=user_id,
    )

    db.add(feedback)
    db.commit()
    db.refresh(feedback)

    return {
        "id": str(feedback.id),
        "feedback_text": feedback.feedback_text,
        "created_at": feedback.created_at,
        "retrieved_at": feedback.retrieved_at,
    }


def mark_instance_completed(
    db: Session, instance_id: UUID, user_id: UUID
) -> dict | None:
    """Mark an agent instance as completed for a specific user"""

    # Check if instance exists and belongs to user
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )
    if not instance:
        return None

    # Update status to completed and set ended_at
    instance.status = AgentStatus.COMPLETED
    instance.ended_at = datetime.now(timezone.utc)

    # Deactivate any pending questions
    db.query(AgentQuestion).filter(
        AgentQuestion.agent_instance_id == instance_id, AgentQuestion.is_active
    ).update({"is_active": False})

    db.commit()

    # Re-query with relationships to ensure they're loaded for _format_instance
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id)
        .options(
            joinedload(AgentInstance.user_agent),
            joinedload(AgentInstance.steps),
            joinedload(AgentInstance.questions),
        )
        .first()
    )

    if not instance:
        return None

    return _format_instance(instance)


def delete_user_account(db: Session, user_id: UUID) -> None:
    """Delete a user account and all associated data in the correct order"""
    logger = logging.getLogger(__name__)

    # Start a transaction
    try:
        # First, cancel any active Stripe subscription
        if settings.stripe_secret_key:
            try:
                import stripe

                stripe.api_key = settings.stripe_secret_key

                subscription = get_or_create_subscription(user_id, db)
                if subscription.provider_subscription_id:
                    logger.info(
                        f"Cancelling Stripe subscription {subscription.provider_subscription_id} for user {user_id}"
                    )
                    # Cancel the subscription immediately
                    stripe_sub = stripe.Subscription.retrieve(
                        subscription.provider_subscription_id
                    )
                    stripe_sub.delete()
            except Exception as e:
                # Log but don't fail - we still want to delete the user
                logger.error(
                    f"Failed to cancel Stripe subscription for user {user_id}: {str(e)}"
                )

        # Delete in order of foreign key dependencies
        # 1. Delete AgentUserFeedback (depends on AgentInstance and User)
        db.query(AgentUserFeedback).filter(
            AgentUserFeedback.created_by_user_id == user_id
        ).delete(synchronize_session=False)

        # 2. Delete AgentQuestions (depends on AgentInstance and User)
        db.query(AgentQuestion).filter(
            AgentQuestion.answered_by_user_id == user_id
        ).delete(synchronize_session=False)

        # Get all agent instances for this user to delete their related data
        instance_ids = [
            instance.id
            for instance in db.query(AgentInstance)
            .filter(AgentInstance.user_id == user_id)
            .all()
        ]

        if instance_ids:
            # Delete questions for user's instances
            db.query(AgentQuestion).filter(
                AgentQuestion.agent_instance_id.in_(instance_ids)
            ).delete(synchronize_session=False)

            # Delete steps for user's instances
            db.query(AgentStep).filter(
                AgentStep.agent_instance_id.in_(instance_ids)
            ).delete(synchronize_session=False)

            # Delete feedback for user's instances
            db.query(AgentUserFeedback).filter(
                AgentUserFeedback.agent_instance_id.in_(instance_ids)
            ).delete(synchronize_session=False)

        # 3. Delete AgentInstances (depends on UserAgent and User)
        db.query(AgentInstance).filter(AgentInstance.user_id == user_id).delete(
            synchronize_session=False
        )

        # 4. Delete UserAgents (depends on User)
        db.query(UserAgent).filter(UserAgent.user_id == user_id).delete(
            synchronize_session=False
        )

        # 5. Delete APIKeys (depends on User)
        db.query(APIKey).filter(APIKey.user_id == user_id).delete(
            synchronize_session=False
        )

        # 6. Delete PushTokens (depends on User)
        db.query(PushToken).filter(PushToken.user_id == user_id).delete(
            synchronize_session=False
        )

        # 7. Delete BillingEvents (depends on User and Subscription)
        db.query(BillingEvent).filter(BillingEvent.user_id == user_id).delete(
            synchronize_session=False
        )

        # 8. Delete Subscription (depends on User)
        db.query(Subscription).filter(Subscription.user_id == user_id).delete(
            synchronize_session=False
        )

        # 9. Finally, delete the User
        db.query(User).filter(User.id == user_id).delete(synchronize_session=False)

        # Commit the transaction
        db.commit()
        logger.info(f"Successfully deleted user {user_id} and all associated data")

    except Exception as e:
        # Rollback on any error
        db.rollback()
        logger.error(f"Failed to delete user {user_id}: {str(e)}")
        raise
