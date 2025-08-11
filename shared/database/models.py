from datetime import datetime, timezone
from uuid import UUID, uuid4
from typing import TYPE_CHECKING

from sqlalchemy import ForeignKey, Index, String, Text, text
from sqlalchemy.dialects.postgresql import UUID as PostgresUUID, JSONB
from sqlalchemy.orm import (
    DeclarativeBase,  # type: ignore[attr-defined]
    Mapped,  # type: ignore[attr-defined]
    mapped_column,  # type: ignore[attr-defined]
    relationship,
    validates,
)

from .enums import AgentStatus, SenderType
from .utils import is_valid_git_diff

if TYPE_CHECKING:
    from .subscription_models import (
        Subscription,
        BillingEvent,
    )


class Base(DeclarativeBase):
    pass


class User(Base):
    __tablename__ = "users"

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True
    )  # Matches Supabase auth.users.id
    email: Mapped[str] = mapped_column(String(255), unique=True)
    display_name: Mapped[str | None] = mapped_column(String(255), default=None)
    created_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    updated_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc),
        onupdate=lambda: datetime.now(timezone.utc),
    )

    # Notification preferences
    push_notifications_enabled: Mapped[bool] = mapped_column(default=True)
    email_notifications_enabled: Mapped[bool] = mapped_column(default=False)
    sms_notifications_enabled: Mapped[bool] = mapped_column(default=False)
    phone_number: Mapped[str | None] = mapped_column(
        String(20), default=None
    )  # E.164 format
    notification_email: Mapped[str | None] = mapped_column(
        String(255), default=None
    )  # Defaults to email if not set

    # Relationships
    agent_instances: Mapped[list["AgentInstance"]] = relationship(
        "AgentInstance", back_populates="user"
    )
    api_keys: Mapped[list["APIKey"]] = relationship("APIKey", back_populates="user")
    user_agents: Mapped[list["UserAgent"]] = relationship(
        "UserAgent", back_populates="user"
    )
    push_tokens: Mapped[list["PushToken"]] = relationship(
        "PushToken", back_populates="user"
    )

    # Billing relationships
    subscription: Mapped["Subscription"] = relationship(
        "Subscription", back_populates="user", uselist=False
    )
    billing_events: Mapped[list["BillingEvent"]] = relationship(
        "BillingEvent", back_populates="user"
    )


class UserAgent(Base):
    __tablename__ = "user_agents"
    __table_args__ = (
        # Partial unique index: only enforce uniqueness for non-deleted agents
        Index(
            "uq_user_agents_user_id_name",
            "user_id",
            "name",
            unique=True,
            postgresql_where=text("is_deleted = FALSE"),
        ),
        Index("ix_user_agents_user_id", "user_id"),
    )

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True)
    )
    name: Mapped[str] = mapped_column(String(255))
    webhook_url: Mapped[str | None] = mapped_column(Text, default=None)
    webhook_api_key: Mapped[str | None] = mapped_column(Text, default=None)  # Encrypted
    is_active: Mapped[bool] = mapped_column(default=True)
    is_deleted: Mapped[bool] = mapped_column(default=False)  # Soft delete flag
    created_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    updated_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc),
        onupdate=lambda: datetime.now(timezone.utc),
    )

    # Relationships
    user: Mapped["User"] = relationship("User", back_populates="user_agents")
    instances: Mapped[list["AgentInstance"]] = relationship(
        "AgentInstance", back_populates="user_agent"
    )


class AgentInstance(Base):
    __tablename__ = "agent_instances"
    __table_args__ = (
        Index("idx_agent_instances_user_agent_id", "user_agent_id"),
        Index("idx_agent_instances_user_status", "user_id", "status"),
    )

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_agent_id: Mapped[UUID] = mapped_column(
        ForeignKey("user_agents.id"), type_=PostgresUUID(as_uuid=True)
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True)
    )
    status: Mapped[AgentStatus] = mapped_column(default=AgentStatus.ACTIVE)
    started_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    ended_at: Mapped[datetime | None] = mapped_column(default=None)
    git_diff: Mapped[str | None] = mapped_column(Text, default=None)
    name: Mapped[str | None] = mapped_column(String(255), default=None)
    last_read_message_id: Mapped[UUID | None] = mapped_column(
        ForeignKey(
            "messages.id",
            use_alter=True,
            name="fk_agent_instances_last_read_message",
            ondelete="SET NULL",
        ),
        type_=PostgresUUID(as_uuid=True),
        default=None,
    )

    # Relationships
    user_agent: Mapped["UserAgent"] = relationship(
        "UserAgent", back_populates="instances"
    )
    user: Mapped["User"] = relationship("User", back_populates="agent_instances")
    messages: Mapped[list["Message"]] = relationship(
        "Message",
        back_populates="instance",
        order_by="Message.created_at",
        foreign_keys="Message.agent_instance_id",
    )
    last_read_message: Mapped["Message | None"] = relationship(
        "Message",
        foreign_keys=[last_read_message_id],
        post_update=True,
        passive_deletes=True,
    )

    @validates("git_diff")
    def validate_git_diff(self, key, value):
        """Validate git diff at the database level.

        Raises ValueError if the git diff is invalid.
        """
        if value is None or value == "":
            return value

        if not is_valid_git_diff(value):
            raise ValueError("Invalid git diff format. Must be a valid unified diff.")

        return value


class APIKey(Base):
    __tablename__ = "api_keys"

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True)
    )
    name: Mapped[str] = mapped_column(String(255))
    api_key_hash: Mapped[str] = mapped_column(String(128))
    api_key: Mapped[str] = mapped_column(
        Text
    )  # Store the actual JWT for user viewing, not good for security
    is_active: Mapped[bool] = mapped_column(default=True)
    created_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    expires_at: Mapped[datetime | None] = mapped_column(default=None)
    last_used_at: Mapped[datetime | None] = mapped_column(default=None)

    # Relationships
    user: Mapped["User"] = relationship("User", back_populates="api_keys")


class PushToken(Base):
    __tablename__ = "push_tokens"

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    user_id: Mapped[UUID] = mapped_column(
        ForeignKey("users.id"), type_=PostgresUUID(as_uuid=True)
    )
    token: Mapped[str] = mapped_column(String(255), unique=True)
    platform: Mapped[str] = mapped_column(String(50))  # 'ios' or 'android'
    is_active: Mapped[bool] = mapped_column(default=True)
    created_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    updated_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc),
        onupdate=lambda: datetime.now(timezone.utc),
    )
    last_used_at: Mapped[datetime | None] = mapped_column(default=None)

    # Relationships
    user: Mapped["User"] = relationship("User", back_populates="push_tokens")


class Message(Base):
    __tablename__ = "messages"
    __table_args__ = (
        Index("idx_messages_instance_created", "agent_instance_id", "created_at"),
    )

    id: Mapped[UUID] = mapped_column(
        PostgresUUID(as_uuid=True), primary_key=True, default=uuid4
    )
    agent_instance_id: Mapped[UUID] = mapped_column(
        ForeignKey("agent_instances.id"), type_=PostgresUUID(as_uuid=True)
    )
    sender_type: Mapped[SenderType] = mapped_column()
    content: Mapped[str] = mapped_column(Text)
    created_at: Mapped[datetime] = mapped_column(
        default=lambda: datetime.now(timezone.utc)
    )
    requires_user_input: Mapped[bool] = mapped_column(default=False)
    message_metadata: Mapped[dict | None] = mapped_column(JSONB, default=None)

    # Relationships
    instance: Mapped["AgentInstance"] = relationship(
        "AgentInstance",
        back_populates="messages",
        foreign_keys=[agent_instance_id],
    )
