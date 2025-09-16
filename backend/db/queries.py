import logging
from datetime import datetime, timezone
from uuid import UUID

from shared.config import settings
from shared.database import (
    AgentInstance,
    AgentStatus,
    AgentInstanceAccess,
    APIKey,
    Message,
    PushToken,
    Team,
    TeamInstanceAccess,
    TeamMembership,
    User,
    UserAgent,
    InstanceAccessLevel,
    TeamRole,
    SenderType,
)
from shared.database.billing_operations import get_or_create_subscription
from shared.database.subscription_models import BillingEvent, Subscription
from sqlalchemy import case, desc, func, or_, select
from sqlalchemy.orm import Session, joinedload, subqueryload, aliased, selectinload

# Import Pydantic models for type-safe returns
from backend.models import (
    AgentInstanceResponse,
    AgentInstanceDetail,
    AgentTypeOverview,
    MessageResponse,
    TeamSummary,
    TeamDetailResponse,
    TeamMemberResponse,
)


ACCESS_PRIORITY = {
    InstanceAccessLevel.READ: 1,
    InstanceAccessLevel.WRITE: 2,
}


def _normalize_email(email: str) -> str:
    """Normalize email for case-insensitive comparisons while preserving original casing elsewhere."""
    return email.strip().lower()


def _message_to_response(msg: Message) -> MessageResponse:
    sender = msg.sender_user
    return MessageResponse(
        id=str(msg.id),
        content=msg.content,
        sender_type=msg.sender_type.value,
        sender_user_id=str(msg.sender_user_id) if msg.sender_user_id else None,
        sender_user_email=sender.email if sender else None,
        sender_user_display_name=sender.display_name if sender else None,
        created_at=msg.created_at,
        requires_user_input=msg.requires_user_input,
    )


def _team_member_to_response(member: TeamMembership) -> TeamMemberResponse:
    user = member.user
    email = user.email if user else member.invited_email or ""
    display_name = user.display_name if user else None
    return TeamMemberResponse(
        id=str(member.id),
        role=member.role,
        user_id=str(member.user_id) if member.user_id else None,
        email=email,
        display_name=display_name,
        invited=member.user_id is None,
        created_at=member.created_at,
        updated_at=member.updated_at,
    )


def _get_team_membership(
    db: Session, team_id: UUID, user_id: UUID
) -> TeamMembership | None:
    return (
        db.query(TeamMembership)
        .options(joinedload(TeamMembership.user))
        .filter(TeamMembership.team_id == team_id, TeamMembership.user_id == user_id)
        .first()
    )


def _get_team(db: Session, team_id: UUID) -> Team | None:
    return db.query(Team).filter(Team.id == team_id).first()


def _get_user_by_email(db: Session, email: str) -> User | None:
    normalized = _normalize_email(email)
    return db.query(User).filter(func.lower(User.email) == normalized).first()


def _compute_effective_instance_access(
    db: Session, instance: AgentInstance, user_id: UUID
) -> InstanceAccessLevel | None:
    if instance.user_id == user_id:
        return InstanceAccessLevel.WRITE

    access_levels: list[InstanceAccessLevel] = []

    direct_access = (
        db.query(AgentInstanceAccess.access)
        .filter(
            AgentInstanceAccess.agent_instance_id == instance.id,
            AgentInstanceAccess.user_id == user_id,
        )
        .first()
    )
    if direct_access and direct_access.access:
        access_levels.append(direct_access.access)

    team_access_rows = (
        db.query(TeamInstanceAccess.access)
        .join(TeamMembership, TeamMembership.team_id == TeamInstanceAccess.team_id)
        .filter(
            TeamInstanceAccess.agent_instance_id == instance.id,
            TeamMembership.user_id == user_id,
        )
        .all()
    )
    for row in team_access_rows:
        if row.access:
            access_levels.append(row.access)

    if not access_levels:
        return None

    return max(access_levels, key=lambda level: ACCESS_PRIORITY[level])


def get_instance_and_access(
    db: Session, instance_id: UUID, user_id: UUID
) -> tuple[AgentInstance | None, InstanceAccessLevel | None]:
    instance = db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
    if not instance:
        return None, None

    access = _compute_effective_instance_access(db, instance, user_id)
    return instance, access


def _get_instance_message_stats(db: Session, instance_ids: list[UUID]) -> dict:
    """
    Efficiently get message statistics for multiple instances.
    Returns a dict mapping instance_id to (latest_message, latest_message_at, message_count)
    """
    if not instance_ids:
        return {}

    # Use a single query with window functions to get both count and latest message
    subquery = (
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
        .filter(Message.agent_instance_id.in_(instance_ids))
        .subquery()
    )

    # Get only the latest message (rn=1) with the count
    results = (
        db.query(
            subquery.c.agent_instance_id,
            subquery.c.content,
            subquery.c.created_at,
            subquery.c.msg_count,
        )
        .filter(subquery.c.rn == 1)
        .all()
    )

    # Convert to dict for easy lookup
    stats = {}
    for row in results:
        stats[row.agent_instance_id] = {
            "latest_message": row.content,
            "latest_message_at": row.created_at,
            "message_count": row.msg_count,
        }

    return stats


def _format_instance(
    instance: AgentInstance, message_stats: dict
) -> AgentInstanceResponse:
    """
    Helper function to format an agent instance consistently.

    Args:
        instance: The AgentInstance to format
        message_stats: Pre-computed message statistics dict mapping instance_id to stats.
                      Each stat should contain 'latest_message', 'latest_message_at', 'message_count'
    """
    # Get stats for this instance, defaulting to empty values if not found
    stats = message_stats.get(instance.id, {})
    latest_message = stats.get("latest_message")
    latest_message_at = stats.get("latest_message_at")
    chat_length = stats.get("message_count", 0)

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
        last_heartbeat_at=instance.last_heartbeat_at,
    )


def get_all_agent_types_with_instances(
    db: Session, user_id: UUID
) -> list[AgentTypeOverview]:
    """Get all non-deleted user agents with their instances for a specific user - OPTIMIZED"""
    # Get all non-deleted user agents for this user with instances in a single query
    user_agents = (
        db.query(UserAgent)
        .filter(UserAgent.user_id == user_id, UserAgent.is_deleted.is_(False))
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
    db: Session, user_id: UUID, limit: int | None = None, scope: str = "me"
) -> list[AgentInstanceResponse]:
    """Get agent instances for a user based on requested visibility scope."""

    scope = (scope or "me").lower()
    if scope not in {"me", "shared", "all"}:
        raise ValueError(f"Invalid agent instance scope: {scope}")

    query = (
        db.query(AgentInstance)
        .filter(AgentInstance.status != AgentStatus.DELETED)
        .options(joinedload(AgentInstance.user_agent))
        .order_by(desc(AgentInstance.started_at))
    )

    if scope == "me":
        query = query.filter(AgentInstance.user_id == user_id)
    else:
        direct_access_select = select(AgentInstanceAccess.agent_instance_id).where(
            AgentInstanceAccess.user_id == user_id
        )
        team_access_select = (
            select(TeamInstanceAccess.agent_instance_id)
            .select_from(TeamInstanceAccess)
            .join(
                TeamMembership,
                TeamMembership.team_id == TeamInstanceAccess.team_id,
            )
            .where(TeamMembership.user_id == user_id)
        )
        shared_instances_select = direct_access_select.union(team_access_select)

        if scope == "shared":
            query = query.filter(
                AgentInstance.user_id != user_id,
                AgentInstance.id.in_(shared_instances_select),
            )
        else:  # scope == "all"
            query = query.filter(
                or_(
                    AgentInstance.user_id == user_id,
                    AgentInstance.id.in_(shared_instances_select),
                )
            )

    if limit is not None:
        query = query.limit(limit)

    instances = query.all()

    # Get all instance IDs for bulk message stats query
    instance_ids = [instance.id for instance in instances]

    # Get message stats for all instances in one efficient query
    message_stats = _get_instance_message_stats(db, instance_ids)

    # Format instances using helper function with pre-computed stats
    return [_format_instance(instance, message_stats) for instance in instances]


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
            UserAgent.user_id == user_id,
            UserAgent.is_deleted.is_(False),
            AgentInstance.status != AgentStatus.DELETED,
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
        .filter(
            UserAgent.id == agent_type_id,
            UserAgent.user_id == user_id,
            UserAgent.is_deleted.is_(False),
        )
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
            joinedload(AgentInstance.user_agent),
        )
        .order_by(desc(AgentInstance.started_at))
        .all()
    )

    # Get all instance IDs for bulk message stats query
    instance_ids = [instance.id for instance in instances]

    # Get message stats for all instances in one efficient query
    message_stats = _get_instance_message_stats(db, instance_ids)

    # Format instances using helper function with pre-computed stats
    return [_format_instance(instance, message_stats) for instance in instances]


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
    messages_query = (
        db.query(Message)
        .options(joinedload(Message.sender_user))
        .filter(Message.agent_instance_id == instance_id)
    )

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
    formatted_messages = [_message_to_response(msg) for msg in messages]

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
        last_heartbeat_at=instance.last_heartbeat_at,
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
        )
        .first()
    )

    if not instance:
        return None

    # Get message stats for this single instance
    message_stats = _get_instance_message_stats(db, [instance.id])

    return _format_instance(instance, message_stats)


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
        )
        .first()
    )

    if not instance:
        return None

    instance.name = name
    db.commit()
    db.refresh(instance)

    # Get message stats for this single instance
    message_stats = _get_instance_message_stats(db, [instance.id])

    # Return the updated instance in the standard format
    return _format_instance(instance, message_stats)


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
) -> list[MessageResponse] | None:
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
    messages_query = (
        db.query(Message)
        .options(joinedload(Message.sender_user))
        .filter(Message.agent_instance_id == instance_id)
    )

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

    # Convert to MessageResponse objects
    return [_message_to_response(msg) for msg in messages]


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


def create_user_message_with_access(
    db: Session,
    instance_id: UUID,
    user: User,
    content: str,
    mark_as_read: bool = False,
) -> MessageResponse:
    """Create a user-authored message after validating write access."""

    instance, access = get_instance_and_access(db, instance_id, user.id)
    if not instance:
        raise ValueError("Agent instance not found")

    if access != InstanceAccessLevel.WRITE:
        raise ValueError("Agent instance not found")

    message = Message(
        agent_instance_id=instance.id,
        sender_type=SenderType.USER,
        sender_user_id=user.id,
        content=content,
        requires_user_input=False,
    )
    db.add(message)
    db.flush()
    db.refresh(message)
    message.sender_user = user

    instance.status = AgentStatus.ACTIVE
    if mark_as_read:
        instance.last_read_message_id = message.id

    try:
        from servers.shared.db.queries import trigger_webhook_for_user_response

        trigger_webhook_for_user_response(
            db=db,
            agent_instance_id=str(instance.id),
            user_message_content=content,
            user_message_id=str(message.id),
            user_id=str(user.id),
        )
    except (
        Exception
    ) as exc:  # pragma: no cover - webhook failures shouldn't block messaging
        logging.getLogger(__name__).exception(
            "Failed to trigger webhook for user response: %s", exc
        )

    return _message_to_response(message)


# ============================================================================
# Team queries
# ============================================================================


def get_user_teams(db: Session, user_id: UUID) -> list[TeamSummary]:
    """Return summary information for teams the user belongs to."""

    membership_alias = aliased(TeamMembership)
    member_count_subq = (
        db.query(
            TeamMembership.team_id.label("team_id"),
            func.count(TeamMembership.id).label("member_count"),
        )
        .group_by(TeamMembership.team_id)
        .subquery()
    )

    results = (
        db.query(
            Team,
            membership_alias.role.label("user_role"),
            member_count_subq.c.member_count,
        )
        .join(membership_alias, membership_alias.team_id == Team.id)
        .join(member_count_subq, member_count_subq.c.team_id == Team.id)
        .filter(membership_alias.user_id == user_id)
        .all()
    )

    summaries: list[TeamSummary] = []
    for team, user_role, member_count in results:
        summaries.append(
            TeamSummary(
                id=str(team.id),
                name=team.name,
                created_at=team.created_at,
                updated_at=team.updated_at,
                role=user_role,
                member_count=member_count or 0,
            )
        )

    return summaries


def get_team_detail(
    db: Session, team_id: UUID, user_id: UUID
) -> TeamDetailResponse | None:
    """Return detailed team info with membership list if user belongs to the team."""

    membership = _get_team_membership(db, team_id, user_id)
    if not membership:
        return None

    team = (
        db.query(Team)
        .options(
            selectinload(Team.memberships).options(joinedload(TeamMembership.user))
        )
        .filter(Team.id == team_id)
        .first()
    )

    if not team:
        return None

    members = sorted(team.memberships, key=lambda m: m.created_at)
    member_responses = [_team_member_to_response(member) for member in members]

    return TeamDetailResponse(
        id=str(team.id),
        name=team.name,
        created_at=team.created_at,
        updated_at=team.updated_at,
        role=membership.role,
        members=member_responses,
    )


def create_team(db: Session, owner: User, name: str) -> TeamDetailResponse:
    """Create a new team and assign the owner membership."""

    team = Team(name=name)
    db.add(team)
    db.flush()

    owner_membership = TeamMembership(
        team_id=team.id,
        user_id=owner.id,
        invited_email=owner.email,
        role=TeamRole.OWNER,
    )
    db.add(owner_membership)
    db.flush()

    db.refresh(team)
    db.refresh(owner_membership)

    return get_team_detail(db, team.id, owner.id)  # type: ignore[arg-type]


def update_team(
    db: Session, team_id: UUID, acting_user_id: UUID, name: str
) -> TeamSummary | None:
    """Rename a team if the acting user is an owner or admin."""

    membership = _get_team_membership(db, team_id, acting_user_id)
    if not membership:
        return None

    if membership.role not in (TeamRole.OWNER, TeamRole.ADMIN):
        raise PermissionError("Only owners or admins can update team details")

    team = _get_team(db, team_id)
    if not team:
        return None

    team.name = name
    db.flush()

    member_count = (
        db.query(func.count(TeamMembership.id))
        .filter(TeamMembership.team_id == team_id)
        .scalar()
        or 0
    )

    return TeamSummary(
        id=str(team.id),
        name=team.name,
        created_at=team.created_at,
        updated_at=team.updated_at,
        role=membership.role,
        member_count=member_count,
    )


def delete_team(db: Session, team_id: UUID, acting_user_id: UUID) -> bool:
    """Delete a team when the acting user is the owner."""

    membership = _get_team_membership(db, team_id, acting_user_id)
    if not membership:
        return False

    if membership.role != TeamRole.OWNER:
        raise PermissionError("Only the team owner can delete the team")

    team = _get_team(db, team_id)
    if not team:
        return False

    db.delete(team)
    return True


def add_team_member(
    db: Session,
    team_id: UUID,
    acting_user_id: UUID,
    email: str,
    role: TeamRole | None = None,
) -> TeamMemberResponse:
    """Add a member to the team by email. Allows placeholder entries when the user does not exist yet."""

    membership = _get_team_membership(db, team_id, acting_user_id)
    if not membership:
        raise PermissionError("Team not found or access denied")

    if membership.role not in (TeamRole.OWNER, TeamRole.ADMIN):
        raise PermissionError("Only owners or admins can add members")

    desired_role = role or TeamRole.MEMBER
    if desired_role == TeamRole.OWNER:
        raise ValueError("Cannot assign OWNER role when adding a member")
    if desired_role == TeamRole.ADMIN and membership.role != TeamRole.OWNER:
        raise PermissionError(
            "Only owners can assign the admin role when inviting members"
        )

    normalized_email = _normalize_email(email)
    existing_user = _get_user_by_email(db, email)

    filters = [func.lower(TeamMembership.invited_email) == normalized_email]
    if existing_user:
        filters.append(TeamMembership.user_id == existing_user.id)

    existing_membership = (
        db.query(TeamMembership)
        .filter(TeamMembership.team_id == team_id)
        .filter(or_(*filters))
        .first()
    )
    if existing_membership:
        raise ValueError("Member with this email is already part of the team")

    membership_entry = TeamMembership(
        team_id=team_id,
        user_id=existing_user.id if existing_user else None,
        invited_email=email.strip(),
        role=desired_role,
    )
    db.add(membership_entry)
    db.flush()
    db.refresh(membership_entry)

    if existing_user:
        membership_entry.user = existing_user

    return _team_member_to_response(membership_entry)


def update_team_member_role(
    db: Session,
    team_id: UUID,
    membership_id: UUID,
    acting_user_id: UUID,
    new_role: TeamRole,
) -> TeamMemberResponse | None:
    """Update a member's role. Restricted to team owners."""

    acting_membership = _get_team_membership(db, team_id, acting_user_id)
    if not acting_membership:
        return None

    if acting_membership.role != TeamRole.OWNER:
        raise PermissionError("Only the team owner can change member roles")

    target_membership = (
        db.query(TeamMembership)
        .options(joinedload(TeamMembership.user))
        .filter(TeamMembership.id == membership_id, TeamMembership.team_id == team_id)
        .first()
    )

    if not target_membership:
        return None

    if target_membership.role == TeamRole.OWNER:
        raise ValueError("Cannot modify the owner role")

    if new_role == TeamRole.OWNER:
        raise ValueError("Cannot promote another member to owner through this endpoint")

    target_membership.role = new_role
    db.flush()
    db.refresh(target_membership)

    return _team_member_to_response(target_membership)


def remove_team_member(
    db: Session,
    team_id: UUID,
    membership_id: UUID,
    acting_user_id: UUID,
) -> bool:
    """Remove a member from the team respecting role restrictions."""

    acting_membership = _get_team_membership(db, team_id, acting_user_id)
    if not acting_membership:
        raise PermissionError("Team not found or access denied")

    if acting_membership.role not in (TeamRole.OWNER, TeamRole.ADMIN):
        raise PermissionError("Only owners or admins can remove members")

    target_membership = (
        db.query(TeamMembership)
        .filter(TeamMembership.id == membership_id, TeamMembership.team_id == team_id)
        .first()
    )

    if not target_membership:
        return False

    if target_membership.role == TeamRole.OWNER:
        raise ValueError("Cannot remove the team owner")

    if (
        acting_membership.role == TeamRole.ADMIN
        and target_membership.role == TeamRole.ADMIN
        and target_membership.user_id != acting_user_id
    ):
        raise PermissionError("Admins can only remove members or themselves")

    db.delete(target_membership)
    return True
