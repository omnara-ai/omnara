import logging
from datetime import datetime, timezone
from uuid import UUID

from shared.config import settings
from shared.database import (
    AgentInstance,
    AgentStatus,
    APIKey,
    Message,
    PushToken,
    SenderType,
    User,
    UserAgent,
)
from shared.database.billing_operations import get_or_create_subscription
from shared.database.subscription_models import BillingEvent, Subscription
from sqlalchemy import case, desc, func
from sqlalchemy.orm import Session, joinedload, subqueryload

# Import Pydantic models for type-safe returns
from backend.models import (
    AgentInstanceResponse,
    AgentInstanceDetail,
    MessageResponse,
    UserMessageResponse,
    AgentTypeOverview,
)


def _format_instance(instance: AgentInstance) -> AgentInstanceResponse:
    """Helper function to format an agent instance consistently"""
    # Get all messages for this instance
    messages = instance.messages if hasattr(instance, "messages") else []

    # Get latest message and its timestamp
    latest_message = None
    latest_message_at = None
    if messages:
        last_msg = max(messages, key=lambda m: m.created_at)
        latest_message = last_msg.content
        latest_message_at = last_msg.created_at

    # Get total message count (chat length)
    chat_length = len(messages)

    return AgentInstanceResponse(
        id=str(instance.id),
        agent_type_id=str(instance.user_agent_id) if instance.user_agent_id else "",
        agent_type_name=instance.user_agent.name if instance.user_agent else "Unknown",
        name=instance.name,
        status=instance.status,
        started_at=instance.started_at,
        ended_at=instance.ended_at,
        latest_message=latest_message,
        latest_message_at=latest_message_at,
        chat_length=chat_length,
    )


def get_all_agent_types_with_instances(
    db: Session, user_id: UUID
) -> list[AgentTypeOverview]:
    """Get all user agents with their instances for a specific user - OPTIMIZED"""
    # Get all user agents for this user with instances in a single query
    user_agents = (
        db.query(UserAgent)
        .filter(UserAgent.user_id == user_id)
        .options(subqueryload(UserAgent.instances))
        .all()
    )

    # Collect all instance IDs for bulk message stats query (excluding DELETED)
    all_instance_ids = []
    for user_agent in user_agents:
        for instance in user_agent.instances:
            if instance.status != AgentStatus.DELETED:
                all_instance_ids.append(instance.id)

    # Get message stats for ALL instances in a simpler, more efficient query
    message_stats = {}
    if all_instance_ids:
        # Use window functions to get both count and latest message in one query
        # This leverages our new index on (agent_instance_id, created_at)

        # Subquery with row_number to identify the latest message per instance
        latest_msg_cte = (
            db.query(
                Message.agent_instance_id,
                Message.content,
                Message.created_at,
                func.row_number()
                .over(
                    partition_by=Message.agent_instance_id,
                    order_by=desc(Message.created_at),
                )
                .label("rn"),
                func.count(Message.id)
                .over(partition_by=Message.agent_instance_id)
                .label("msg_count"),
            )
            .filter(Message.agent_instance_id.in_(all_instance_ids))
            .subquery()
        )

        # Get only the latest message (rn=1) with counts
        stats_results = (
            db.query(
                latest_msg_cte.c.agent_instance_id,
                latest_msg_cte.c.content,
                latest_msg_cte.c.created_at,
                latest_msg_cte.c.msg_count,
            )
            .filter(latest_msg_cte.c.rn == 1)
            .all()
        )

        for row in stats_results:
            message_stats[row.agent_instance_id] = {
                "count": row.msg_count or 0,
                "latest_at": row.created_at,
                "latest_content": row.content,
            }

    result = []
    for user_agent in user_agents:
        # Filter out DELETED instances
        instances = [i for i in user_agent.instances if i.status != AgentStatus.DELETED]

        # Create a list of instances with their stats
        instances_with_stats = []
        for instance in instances:
            stats = message_stats.get(instance.id, {})
            instances_with_stats.append(
                {
                    "instance": instance,
                    "message_count": stats.get("count", 0),
                    "latest_message_at": stats.get("latest_at"),
                    "latest_message": stats.get("latest_content"),
                }
            )

        # Sort instances: AWAITING_INPUT instances first, then by most recent activity
        def sort_key(item):
            instance = item["instance"]
            latest_at = item["latest_message_at"]

            # If instance is awaiting input, prioritize it
            if instance.status == AgentStatus.AWAITING_INPUT:
                # Sort by when the question was asked (last message time)
                if latest_at:
                    return (0, latest_at)
                else:
                    return (0, instance.started_at)

            # Otherwise sort by last activity
            last_activity = latest_at if latest_at else instance.started_at
            return (1, -last_activity.timestamp())

        sorted_items = sorted(instances_with_stats, key=sort_key)

        # Format instances with optimized data
        formatted_instances = []
        for item in sorted_items:
            instance = item["instance"]
            formatted_instances.append(
                AgentInstanceResponse(
                    id=str(instance.id),
                    agent_type_id=str(instance.user_agent_id)
                    if instance.user_agent_id
                    else "",
                    agent_type_name=instance.user_agent.name
                    if instance.user_agent
                    else "Unknown",
                    name=instance.name,
                    status=instance.status,
                    started_at=instance.started_at,
                    ended_at=instance.ended_at,
                    latest_message=item["latest_message"],
                    latest_message_at=item["latest_message_at"],
                    chat_length=item["message_count"],
                )
            )

        result.append(
            AgentTypeOverview(
                id=str(user_agent.id),
                name=user_agent.name,
                created_at=user_agent.created_at,
                recent_instances=formatted_instances,
                total_instances=len(instances),
                active_instances=sum(
                    1 for i in instances if i.status == AgentStatus.ACTIVE
                ),
            )
        )

    return result


def get_all_agent_instances(
    db: Session, user_id: UUID, limit: int | None = None
) -> list[AgentInstanceResponse]:
    """Get all agent instances for a specific user, sorted by most recent activity"""

    query = (
        db.query(AgentInstance)
        .filter(
            AgentInstance.user_id == user_id,
            AgentInstance.status != AgentStatus.DELETED,
        )
        .options(
            joinedload(AgentInstance.messages),
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

    # Single query to get all counts using conditional aggregation (excluding DELETED)
    stats = (
        db.query(
            func.count(AgentInstance.id).label("total"),
            func.count(case((AgentInstance.status == AgentStatus.ACTIVE, 1))).label(
                "active"
            ),
            func.count(case((AgentInstance.status == AgentStatus.COMPLETED, 1))).label(
                "completed"
            ),
        )
        .filter(
            AgentInstance.user_id == user_id,
            AgentInstance.status != AgentStatus.DELETED,
        )
        .first()
    )

    # Handle the case where stats might be None (though COUNT queries always return a row)
    if stats:
        total_instances = stats.total or 0
        active_instances = stats.active or 0
        completed_instances = stats.completed or 0
    else:
        total_instances = 0
        active_instances = 0
        completed_instances = 0

    # Count by user agent and status (for fleet overview, excluding DELETED)
    # Get instances with their user agents
    agent_type_stats = (
        db.query(
            UserAgent.id,
            UserAgent.name,
            AgentInstance.status,
            func.count(AgentInstance.id).label("count"),
        )
        .join(AgentInstance, AgentInstance.user_agent_id == UserAgent.id)
        .filter(
            UserAgent.user_id == user_id, AgentInstance.status != AgentStatus.DELETED
        )
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
) -> list[AgentInstanceResponse] | None:
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
            AgentInstance.status != AgentStatus.DELETED,
        )
        .options(
            joinedload(AgentInstance.messages),
            joinedload(AgentInstance.user_agent),
        )
        .order_by(desc(AgentInstance.started_at))
        .all()
    )

    # Format instances using helper function
    return [_format_instance(instance) for instance in instances]


def get_agent_instance_detail(
    db: Session,
    instance_id: UUID,
    user_id: UUID,
    message_limit: int | None = None,
    before_message_id: UUID | None = None,
) -> AgentInstanceDetail | None:
    """Get detailed information about a specific agent instance for a specific user with optional message pagination using cursor"""

    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .options(
            joinedload(AgentInstance.user_agent),
        )
        .first()
    )

    if not instance:
        return None

    # Build message query
    messages_query = db.query(Message).filter(Message.agent_instance_id == instance_id)

    # If cursor provided, get messages before that message
    if before_message_id:
        cursor_message = (
            db.query(Message.created_at).filter(Message.id == before_message_id).first()
        )
        if cursor_message:
            messages_query = messages_query.filter(
                Message.created_at < cursor_message.created_at
            )

    # Order by created_at DESC and apply limit
    messages_query = messages_query.order_by(desc(Message.created_at))
    if message_limit is not None:
        messages_query = messages_query.limit(message_limit)

    messages = messages_query.all()

    # Reverse to get chronological order (oldest first) for display
    messages = list(reversed(messages))

    # Format messages for chat display
    formatted_messages = []
    for msg in messages:
        formatted_messages.append(
            MessageResponse(
                id=str(msg.id),
                content=msg.content,
                sender_type=msg.sender_type.value,  # "agent" or "user"
                created_at=msg.created_at,
                requires_user_input=msg.requires_user_input,
            )
        )

    return AgentInstanceDetail(
        id=str(instance.id),
        agent_type_id=str(instance.user_agent_id) if instance.user_agent_id else "",
        agent_type_name=instance.user_agent.name if instance.user_agent else "Unknown",
        status=instance.status,
        started_at=instance.started_at,
        ended_at=instance.ended_at,
        git_diff=instance.git_diff,
        messages=formatted_messages,
        last_read_message_id=str(instance.last_read_message_id)
        if instance.last_read_message_id
        else None,
    )


def submit_user_message(
    db: Session, instance_id: UUID, content: str, user_id: UUID
) -> UserMessageResponse | None:
    """Submit a user message to an agent instance"""

    # Check if instance exists and belongs to user
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )
    if not instance:
        return None

    # Create new user message
    user_message = Message(
        agent_instance_id=instance_id,
        sender_type=SenderType.USER,
        content=content,
        requires_user_input=False,
    )
    db.add(user_message)

    if instance.status != AgentStatus.COMPLETED:
        instance.status = AgentStatus.ACTIVE

    db.commit()
    db.refresh(user_message)

    return UserMessageResponse(
        id=str(user_message.id),
        content=user_message.content,
        sender_type=user_message.sender_type.value,
        created_at=user_message.created_at,
        requires_user_input=user_message.requires_user_input,
    )


def mark_instance_completed(
    db: Session, instance_id: UUID, user_id: UUID
) -> AgentInstanceResponse | None:
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

    # No need to deactivate questions - they're handled by checking for user responses

    db.commit()

    # Re-query with relationships to ensure they're loaded for _format_instance
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id)
        .options(
            joinedload(AgentInstance.user_agent),
            joinedload(AgentInstance.messages),
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
        # Get all agent instances for this user to delete their related data
        instance_ids = [
            instance.id
            for instance in db.query(AgentInstance)
            .filter(AgentInstance.user_id == user_id)
            .all()
        ]

        if instance_ids:
            # Delete messages for user's instances
            db.query(Message).filter(
                Message.agent_instance_id.in_(instance_ids)
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


def delete_agent_instance(db: Session, instance_id: UUID, user_id: UUID) -> bool:
    """Soft delete an agent instance for a specific user"""

    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )

    if not instance:
        return False

    # Delete related messages to save space
    db.query(Message).filter(Message.agent_instance_id == instance_id).delete()

    # Soft delete: mark as DELETED instead of actually deleting
    instance.status = AgentStatus.DELETED
    db.commit()

    return True


def update_agent_instance_name(
    db: Session, instance_id: UUID, user_id: UUID, name: str
) -> AgentInstanceResponse | None:
    """Update the name of an agent instance for a specific user"""

    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .options(
            joinedload(AgentInstance.user_agent),
            joinedload(AgentInstance.messages),
        )
        .first()
    )

    if not instance:
        return None

    instance.name = name
    db.commit()
    db.refresh(instance)

    # Return the updated instance in the standard format
    return _format_instance(instance)


def get_message_by_id(db: Session, message_id: UUID, user_id: UUID) -> dict | None:
    """
    Get a single message by ID with user authorization check.
    Returns the message data if authorized, None if not found or unauthorized.
    """
    message = (
        db.query(Message)
        .join(AgentInstance, Message.agent_instance_id == AgentInstance.id)
        .filter(Message.id == message_id, AgentInstance.user_id == user_id)
        .first()
    )

    if not message:
        return None

    return {
        "id": str(message.id),
        "agent_instance_id": str(message.agent_instance_id),
        "sender_type": message.sender_type.value,
        "content": message.content,
        "created_at": message.created_at.isoformat() + "Z",
        "requires_user_input": message.requires_user_input,
        "message_metadata": message.message_metadata,
    }


def get_instance_messages(
    db: Session,
    instance_id: UUID,
    user_id: UUID,
    limit: int = 50,
    before_message_id: UUID | None = None,
) -> list[dict] | None:
    """
    Get paginated messages for an agent instance using cursor-based pagination.
    Returns list of messages if authorized, None if not found or unauthorized.
    """
    # Verify instance belongs to user
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )

    if not instance:
        return None

    # Build message query
    messages_query = db.query(Message).filter(Message.agent_instance_id == instance_id)

    # If cursor provided, get messages before that message
    if before_message_id:
        cursor_message = (
            db.query(Message.created_at).filter(Message.id == before_message_id).first()
        )
        if cursor_message:
            messages_query = messages_query.filter(
                Message.created_at < cursor_message.created_at
            )

    # Order by created_at DESC and apply limit
    messages = messages_query.order_by(desc(Message.created_at)).limit(limit).all()

    # Reverse to get chronological order
    messages = list(reversed(messages))

    # Format messages
    formatted_messages = []
    for msg in messages:
        formatted_messages.append(
            {
                "id": str(msg.id),
                "content": msg.content,
                "sender_type": msg.sender_type.value,
                "created_at": msg.created_at.isoformat() + "Z",
                "requires_user_input": msg.requires_user_input,
            }
        )

    return formatted_messages


def get_instance_git_diff(db: Session, instance_id: UUID, user_id: UUID) -> dict | None:
    """
    Get the git diff for an agent instance with user authorization check.
    Returns the git diff data if authorized, None if not found or unauthorized.
    """
    instance = (
        db.query(AgentInstance)
        .filter(AgentInstance.id == instance_id, AgentInstance.user_id == user_id)
        .first()
    )

    if not instance:
        return None

    return {"instance_id": str(instance.id), "git_diff": instance.git_diff}
