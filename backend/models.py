"""
Backend API models for Agent Dashboard.

This module contains all Pydantic models used for API request/response serialization.
Models are organized by functional area: questions, agents, billing, and detailed views.
"""

from datetime import datetime
from typing import Optional
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, field_serializer
from shared.database.enums import AgentStatus

# ============================================================================
# Question Models
# ============================================================================


# This is when the Agent prompts the user for an answer and this format
# is what the user responds with.
class AnswerRequest(BaseModel):
    answer: str = Field(..., description="User's answer to the question")


# User feedback that agents can retrieve during their operations
class UserFeedbackRequest(BaseModel):
    feedback: str = Field(..., description="User's feedback or additional information")


class UserFeedbackResponse(BaseModel):
    id: str
    feedback_text: str
    created_at: datetime
    retrieved_at: datetime | None

    @field_serializer("created_at", "retrieved_at")
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


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


# Represents individual steps/actions taken by an agent
class AgentStepResponse(BaseModel):
    id: str
    step_number: int
    description: str
    created_at: datetime

    @field_serializer("created_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# Summary view of an agent instance (a single agent session/run)
class AgentInstanceResponse(BaseModel):
    id: str
    agent_type_id: str
    agent_type_name: str | None = None
    status: AgentStatus
    started_at: datetime
    ended_at: datetime | None
    latest_step: str | None = None
    has_pending_question: bool = False
    pending_question_age: int | None = None  # Age in seconds
    pending_questions_count: int = 0
    step_count: int = 0

    @field_serializer("started_at", "ended_at")
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


# Detailed information about a question asked by an agent, including answer status
class QuestionDetail(BaseModel):
    id: str
    question_text: str
    answer_text: str | None
    asked_at: datetime
    answered_at: datetime | None
    is_active: bool

    @field_serializer("asked_at", "answered_at")
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# Complete detailed view of a specific agent instance
# with full step and question history
class AgentInstanceDetail(BaseModel):
    id: str
    agent_type_id: str
    agent_type: AgentTypeOverview
    status: AgentStatus
    started_at: datetime
    ended_at: datetime | None
    git_diff: str | None = None
    steps: list[AgentStepResponse] = []
    questions: list[QuestionDetail] = []
    user_feedback: list[UserFeedbackResponse] = []

    @field_serializer("started_at", "ended_at")
    def serialize_datetime(self, dt: datetime | None, _info):
        if dt is None:
            return None
        return dt.isoformat() + "Z"

    model_config = ConfigDict(from_attributes=True)


# ============================================================================
# User Agent Models
# ============================================================================


class UserAgentRequest(BaseModel):
    name: str = Field(..., description="Name of the user agent")
    webhook_url: str | None = Field(
        None, description="Webhook URL for remote agent triggering"
    )
    webhook_api_key: str | None = Field(
        None, description="API key for webhook authentication"
    )
    is_active: bool = Field(True, description="Whether the agent is active")


class UserAgentResponse(BaseModel):
    id: str
    name: str
    webhook_url: str | None
    is_active: bool
    created_at: datetime
    updated_at: datetime
    instance_count: int = 0
    active_instance_count: int = 0
    waiting_instance_count: int = 0
    completed_instance_count: int = 0
    error_instance_count: int = 0
    has_webhook: bool = Field(default=False)

    @field_serializer("created_at", "updated_at")
    def serialize_datetime(self, dt: datetime, _info):
        return dt.isoformat() + "Z"

    def __init__(self, **data):
        super().__init__(**data)
        # Compute has_webhook based on webhook_url presence
        self.has_webhook = bool(self.webhook_url)

    model_config = ConfigDict(from_attributes=True)


class CreateAgentInstanceRequest(BaseModel):
    prompt: str = Field(..., description="Initial prompt for the agent")
    name: str | None = Field(None, description="Display name for the agent instance")
    worktree_name: str | None = Field(
        None, description="Git worktree name for the agent"
    )


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
