"""
MCP Server tool interface models.

This module contains all Pydantic models for MCP tool requests and responses.
Models define the interface contract between AI agents and the MCP server.
"""

from pydantic import BaseModel, Field

from servers.shared.models import (
    BaseLogStepRequest,
    BaseLogStepResponse,
    BaseAskQuestionRequest,
    BaseEndSessionRequest,
    BaseEndSessionResponse,
)

# ============================================================================
# Tool Request Models
# ============================================================================


# MCP uses the base models directly for requests
class LogStepRequest(BaseLogStepRequest):
    """MCP-specific request model for logging a step"""

    pass


class AskQuestionRequest(BaseAskQuestionRequest):
    """MCP-specific request model for asking a question"""

    pass


class EndSessionRequest(BaseEndSessionRequest):
    """MCP-specific request model for ending a session"""

    pass


# ============================================================================
# Tool Response Models
# ============================================================================


# MCP uses the base model directly for log step response
class LogStepResponse(BaseLogStepResponse):
    """MCP-specific response model for log step"""

    pass


# MCP-specific: Response contains the answer (blocking operation)
class AskQuestionResponse(BaseModel):
    """MCP-specific response model for ask question"""

    answer: str = Field(..., description="User's answer to the question")
    question_id: str = Field(..., description="ID of the question that was answered")


# MCP uses the base model directly for end session response
class EndSessionResponse(BaseEndSessionResponse):
    """MCP-specific response model for end session"""

    pass
