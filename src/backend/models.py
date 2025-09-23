"""
Backend API models for Agent Dashboard.

This module contains all Pydantic models used for API request/response serialization.
Models are organized by functional area: questions, agents, billing, and detailed views.
"""

from datetime import datetime
from typing import Optional
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, field_serializer, field_validator
from shared.database.enums import AgentStatus, TeamRole, InstanceAccessLevel
from shared.webhook_schemas import (
    get_webhook_type_schema,
    validate_webhook_config as validate_webhook_config_func,
    WEBHOOK_TYPES,
)

# ============================================================================
# Message Models
# ============================================================================


class UserMessageRequest(BaseModel):
    content: str = Field(..., description="Message content from the user")


# ============================================================================
# User Settings Models
# ============================================================================


class UserNotificationSettingsRequest(BaseModel):
    push_notifications_enabled: Optional[bool] = None
    email_notifications_enabled: Optional[bool] = None
    sms_notifications_enabled: Optional[bool] = None
    phone_number: Optional[str] = Field(
        None, description="Phone number in E.164 format (e.g., +1234567890)"
    )
    notification_email: Optional[str] = Field(
        None, description="Email for notifications (defaults to account email)"
    )


class UserNotificationSettingsResponse(BaseModel):
    push_notifications_enabled: bool
    email_notifications_enabled: bool
    sms_notifications_enabled: bool
    phone_number: Optional[str]
    notification_email: str  # Always returns an email (account email as fallback)

    model_config = ConfigDict(from_attributes=True)


# ============================================================================
# Agent Models
# ============================================================================


# Summary view of an agent instance (a single agent session/run)
class AgentInstanceResponse(BaseModel):
    id: str
    agent_type_id: str
    agent_type_name: str | None = None
    name: str | None = None
    status: AgentStatus
    started_at: datetime
    ended_at: datetime | None
    latest_message: str | None = None
    latest_message_at: datetime | None = None  # Timestamp of the latest message
    chat_length: int = 0  # Total message count
    last_heartbeat_at: datetime | None = None

    @field_serializer(
        "started_at", "ended_at", "latest_message_at", "last_heartbeat_at"
    )
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# Overview of an agent type with recent instances
# and summary statistics for dashboard cards
class AgentTypeOverview(BaseModel):
    id: str
    name: str
    created_at: datetime
    recent_instances: list[AgentInstanceResponse] = []
    total_instances: int = 0
    active_instances: int = 0

    @field_serializer("created_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# ============================================================================
# Detailed Views
# ============================================================================


# Message model for the chat interface
class MessageResponse(BaseModel):
    id: str
    content: str
    sender_type: str
    sender_user_id: str | None = None
    sender_user_email: str | None = None
    sender_user_display_name: str | None = None
    created_at: datetime
    requires_user_input: bool
    message_metadata: dict | None = None
    message_type: str | None = None

    @field_serializer("created_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# Complete detailed view of a specific agent instance
# with full message history
class AgentInstanceDetail(BaseModel):
    id: str
    agent_type_id: str
    agent_type_name: str
    status: AgentStatus
    started_at: datetime
    ended_at: datetime | None
    git_diff: str | None = None
    messages: list[MessageResponse] = []
    last_read_message_id: str | None = None
    last_heartbeat_at: datetime | None = None
    access_level: InstanceAccessLevel = InstanceAccessLevel.WRITE
    is_owner: bool = False

    @field_serializer("started_at", "ended_at", "last_heartbeat_at")
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# ============================================================================
# Team Models
# ============================================================================


class InstanceShareCreateRequest(BaseModel):
    email: str = Field(..., description="Email address to grant access")
    access: InstanceAccessLevel = Field(
        default=InstanceAccessLevel.READ,
        description="Access level to grant",
    )


class InstanceShareResponse(BaseModel):
    id: str
    email: str
    access: InstanceAccessLevel
    user_id: str | None = None
    display_name: str | None = None
    invited: bool = False
    is_owner: bool = False
    created_at: datetime
    updated_at: datetime

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


class TeamCreateRequest(BaseModel):
    name: str = Field(..., description="Team name")


class TeamUpdateRequest(BaseModel):
    name: str = Field(..., description="Updated team name")


class TeamMemberAddRequest(BaseModel):
    email: str = Field(..., description="Email address of member to add")
    role: TeamRole | None = Field(
        default=None,
        description="Role for the member (defaults to MEMBER if omitted)",
    )


class TeamMemberRoleUpdateRequest(BaseModel):
    role: TeamRole


class TeamSummary(BaseModel):
    id: str
    name: str
    created_at: datetime
    updated_at: datetime
    role: TeamRole
    member_count: int

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


class TeamMemberResponse(BaseModel):
    id: str
    role: TeamRole
    user_id: str | None = None
    email: str
    display_name: str | None = None
    invited: bool
    created_at: datetime
    updated_at: datetime

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


class TeamDetailResponse(BaseModel):
    id: str
    name: str
    created_at: datetime
    updated_at: datetime
    role: TeamRole
    members: list[TeamMemberResponse]

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# ============================================================================
# User Agent Models
# ============================================================================


class UserAgentRequest(BaseModel):
    name: str = Field(..., description="Name of the user agent")
    webhook_type: str | None = Field(None, description="Type of webhook integration")
    webhook_config: dict | None = Field(None, description="Webhook configuration")
    is_active: bool = Field(True, description="Whether the agent is active")

    @field_validator("webhook_type")
    @classmethod
    def validate_webhook_type(cls, v: str | None) -> str | None:
        """Validate that the webhook type is supported."""
        if v is None:
            return None

        if not get_webhook_type_schema(v):
            supported = ", ".join(WEBHOOK_TYPES.keys())
            raise ValueError(
                f"Unknown webhook type: {v}. Supported types are: {supported}"
            )

        return v

    @field_validator("webhook_config")
    @classmethod
    def validate_webhook_config(cls, v: dict | None, info) -> dict | None:
        """Validate webhook configuration against the webhook type schema."""
        if v is None:
            return None

        # Get the webhook_type from the data
        webhook_type = info.data.get("webhook_type")

        # If no webhook_type, can't validate config
        if not webhook_type:
            return v

        # Validate the configuration
        is_valid, error_msg = validate_webhook_config_func(webhook_type, v)
        if not is_valid:
            raise ValueError(f"Invalid webhook configuration: {error_msg}")

        return v


class UserAgentResponse(BaseModel):
    id: str
    name: str
    webhook_type: str | None = None
    webhook_config: dict | None = None
    is_active: bool
    created_at: datetime
    updated_at: datetime
    instance_count: int = 0
    active_instance_count: int = 0
    waiting_instance_count: int = 0
    completed_instance_count: int = 0
    error_instance_count: int = 0

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


class CreateAgentInstanceRequest(BaseModel):
    """Request to create a new agent instance with dynamic runtime fields based on webhook type."""

    name: str | None = Field(
        None, description="Optional display name for the agent instance"
    )

    # Accept any additional fields dynamically based on the webhook's runtime_fields
    # (e.g., prompt, worktree_name, branch_name for OMNARA_SERVE)
    model_config = ConfigDict(extra="allow")


class WebhookTriggerResponse(BaseModel):
    success: bool
    agent_instance_id: str | None = None
    message: str
    error: str | None = None


# ============================================================================
# Billing Models
# ============================================================================


class SubscriptionResponse(BaseModel):
    """User's subscription details."""

    id: UUID
    plan_type: str
    agent_limit: int
    current_period_end: Optional[datetime] = None
    cancel_at_period_end: bool = False
    provider: Optional[str] = (
        None  # 'stripe', 'apple', 'google', or None for free users
    )

    @field_serializer("current_period_end")
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"


class CreateCheckoutSessionRequest(BaseModel):
    """Request to create a Stripe checkout session."""

    plan_type: str = Field(..., description="Plan type: 'free', 'pro', or 'enterprise'")
    success_url: str = Field(
        ..., description="URL to redirect after successful payment"
    )
    cancel_url: str = Field(..., description="URL to redirect if payment is cancelled")
    promo_code: Optional[str] = Field(None, description="Promotional code to apply")


class CheckoutSessionResponse(BaseModel):
    """Response containing Stripe checkout session details."""

    checkout_url: str
    session_id: str


class UsageResponse(BaseModel):
    """Current usage statistics for the billing period."""

    total_agents: int  # Total agents created this month
    agent_limit: int
    period_start: datetime
    period_end: datetime

    @field_serializer("period_start", "period_end")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"


class ValidatePromoCodeRequest(BaseModel):
    """Request to validate a promotional code."""

    code: str = Field(..., description="The promotional code to validate")
    plan_type: str = Field(..., description="Plan type the code will be applied to")


class PromoCodeValidationResponse(BaseModel):
    """Response with promo code validation details."""

    valid: bool
    code: Optional[str] = None
    discount_type: Optional[str] = None  # 'percentage' or 'amount'
    discount_value: Optional[float] = None
    description: Optional[str] = None
    error: Optional[str] = None


# ============================================================================
# Mobile Subscription Models
# ============================================================================


class MobileSubscriptionActivateRequest(BaseModel):
    """Request to activate a mobile subscription after purchase."""

    provider: str = Field(..., description="Payment provider: 'apple' or 'google'")
    provider_customer_id: str = Field(
        ..., description="Customer ID (user ID in RevenueCat)"
    )
    provider_subscription_id: str = Field(
        ..., description="Apple/Google transaction ID"
    )
    product_id: str = Field(
        ..., description="Product identifier (e.g., com.omnara.app.Monthly)"
    )


class MobileSubscriptionResponse(BaseModel):
    """Response for mobile subscription activation."""

    success: bool
    message: str
    subscription: Optional[SubscriptionResponse] = None
