"""Base models shared between MCP and FastAPI servers.

These models define the common interface for agent operations,
allowing each server to extend them as needed.
"""

from pydantic import BaseModel, Field


# ============================================================================
# Request Models
# ============================================================================


class BaseLogStepRequest(BaseModel):
    """Base request model for logging a step."""

    agent_instance_id: str | None = Field(
        None,
        description="Existing agent instance ID. If not provided, creates a new instance.",
    )
    agent_type: str = Field(
        ..., description="Type of agent (e.g., 'Claude Code', 'Cursor')"
    )
    step_description: str = Field(
        ..., description="Clear description of what the agent is doing"
    )


class BaseAskQuestionRequest(BaseModel):
    """Base request model for asking a question."""

    agent_instance_id: str = Field(..., description="Agent instance ID")
    question_text: str = Field(..., description="Question to ask the user")


class BaseEndSessionRequest(BaseModel):
    """Base request model for ending a session."""

    agent_instance_id: str = Field(..., description="Agent instance ID to end")


# ============================================================================
# Response Models
# ============================================================================


class BaseLogStepResponse(BaseModel):
    """Base response model for log step."""

    success: bool = Field(..., description="Whether the step was logged successfully")
    agent_instance_id: str = Field(
        ..., description="Agent instance ID (new or existing)"
    )
    step_number: int = Field(..., description="Sequential step number")
    user_feedback: list[str] = Field(
        default_factory=list,
        description="List of unretrieved user feedback messages",
    )


class BaseEndSessionResponse(BaseModel):
    """Base response model for end session."""

    success: bool = Field(..., description="Whether the session was ended successfully")
    agent_instance_id: str = Field(..., description="Agent instance ID that was ended")
    final_status: str = Field(
        ..., description="Final status of the session (should be 'completed')"
    )
