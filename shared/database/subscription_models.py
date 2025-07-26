from datetime import UTC, datetime
from uuid import UUID, uuid4
from typing import TYPE_CHECKING

from sqlalchemy import ForeignKey, String, Text, Integer
from sqlalchemy.dialects.postgresql import UUID as PostgresUUID
from sqlalchemy.orm import (
    Mapped,
    mapped_column,
    relationship,
)

from .models import Base

if TYPE_CHECKING:
    from .models import User


class Subscription(Base):
    """
    Optional subscription tracking for hosted deployments.
    Defaults provide unlimited access for open source users.
    """

    __tablename__ = "subscriptions"

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True), unique=True
    )

    # Plan information - default to free
    plan_type: Mapped[str] = mapped_column(String(50), default="free")

    # Limits - -1 means unlimited (default to free plan limits)
    agent_limit: Mapped[int] = mapped_column(
        Integer, default=20
    )  # Free plan: 20 agents/month

    # Payment provider information - minimal needed for operations
    provider: Mapped[str | None] = mapped_column(
        String(20), nullable=True, default=None
    )  # 'stripe', 'apple', 'google', or None for free users
    provider_customer_id: Mapped[str | None] = mapped_column(String(255), default=None)
    provider_subscription_id: Mapped[str | None] = mapped_column(
        String(255), default=None
    )

    # Timestamps
    created_at: Mapped[datetime] = mapped_column(default=lambda: datetime.now(UTC))
    updated_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(UTC), onupdate=lambda: datetime.now(UTC)
    )

    # Relationships
    user: Mapped["User"] = relationship("User", back_populates="subscription")
    billing_events: Mapped[list["BillingEvent"]] = relationship(
        "BillingEvent", back_populates="subscription"
    )


class BillingEvent(Base):
    """
    Audit trail for billing-related events.
    Useful for debugging and customer support.
    """

    __tablename__ = "billing_events"

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True)
    )
    subscription_id: Mapped[UUID | None] = mapped_column(
        ForeignKey("subscriptions.id"), type_=PostgresUUID(as_uuid=True), default=None
    )

    # Event information
    event_type: Mapped[str] = mapped_column(
        String(100)
    )  # 'subscription_created', 'payment_failed', etc.
    event_data: Mapped[str | None] = mapped_column(Text, default=None)  # JSON data
    provider_event_id: Mapped[str | None] = mapped_column(String(255), default=None)

    # Timestamps
    occurred_at: Mapped[datetime] = mapped_column(default=lambda: datetime.now(UTC))

    # Relationships
    user: Mapped["User"] = relationship("User", back_populates="billing_events")
    subscription: Mapped["Subscription"] = relationship(
        "Subscription", back_populates="billing_events"
    )
