"""Shared billing operations for usage limit enforcement."""

from datetime import date
from uuid import UUID

from fastapi import HTTPException
from sqlalchemy.orm import Session

from shared.config import settings
from shared.database import Subscription, AgentInstance, UserAgent, BillingEvent


class UsageLimitError(HTTPException):
    """Raised when a usage limit is exceeded."""

    def __init__(self, message: str, limit_type: str, current: int, limit: int):
        super().__init__(
            status_code=402,  # Payment Required
            detail={
                "message": message,
                "limit_type": limit_type,
                "current": current,
                "limit": limit,
                "upgrade_required": True,
            },
        )


def count_monthly_agents(user_id: UUID, db: Session) -> int:
    """Count the total number of agents created in the current month."""
    today = date.today()
    month_start = today.replace(day=1)

    # Count all agent instances created this month
    monthly_count = (
        db.query(AgentInstance)
        .join(UserAgent)
        .filter(UserAgent.user_id == user_id, AgentInstance.started_at >= month_start)
        .count()
    )

    return monthly_count


def check_agent_limit(user_id: UUID, db: Session, increment: int = 1) -> bool:
    """
    Check if user can create more agents based on their subscription.

    Args:
        user_id: The user's ID
        db: Database session
        increment: How many agents they want to add (default 1)

    Returns:
        True if within limits

    Raises:
        UsageLimitError if limit would be exceeded
    """
    if not settings.enforce_limits:
        return True

    subscription = get_or_create_subscription(user_id, db)

    # Unlimited plan
    if subscription.agent_limit == -1:
        return True

    # Count monthly agents
    monthly_agents = count_monthly_agents(user_id, db)

    # Check if adding new agents would exceed limit
    if monthly_agents + increment > subscription.agent_limit:
        raise UsageLimitError(
            message="Monthly agent limit exceeded. Upgrade to create more agents.",
            limit_type="agents",
            current=monthly_agents,
            limit=subscription.agent_limit,
        )

    return True


def get_or_create_subscription(user_id: UUID, db: Session) -> Subscription:
    """Get existing subscription or create a default free one."""
    subscription = db.query(Subscription).filter_by(user_id=user_id).first()
    if not subscription:
        # Create with defaults from model (plan_type="free", agent_limit=20)
        subscription = Subscription(user_id=user_id)
        db.add(subscription)
        db.commit()
        db.refresh(subscription)
    return subscription


def find_subscription_by_customer_id(
    customer_id: str, db: Session
) -> Subscription | None:
    """Find subscription by Stripe customer ID."""
    return db.query(Subscription).filter_by(provider_customer_id=customer_id).first()


def find_subscription_by_provider_id(
    provider_subscription_id: str, db: Session
) -> Subscription | None:
    """Find subscription by Stripe subscription ID."""
    return (
        db.query(Subscription)
        .filter_by(provider_subscription_id=provider_subscription_id)
        .first()
    )


def update_subscription_customer_id(
    subscription: Subscription, customer_id: str, db: Session
) -> None:
    """Update subscription with Stripe customer ID."""
    subscription.provider_customer_id = customer_id
    db.commit()


def create_billing_event(
    user_id: UUID,
    subscription_id: UUID | None,
    event_type: str,
    event_data: str | None,
    provider_event_id: str | None,
    db: Session,
) -> BillingEvent:
    """Create a billing event for audit trail."""
    event = BillingEvent(
        user_id=user_id,
        subscription_id=subscription_id,
        event_type=event_type,
        event_data=event_data,
        provider_event_id=provider_event_id,
    )
    db.add(event)
    db.commit()
    return event
