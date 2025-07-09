"""Pydantic models for FastAPI request/response schemas."""

from typing import Optional

from pydantic import BaseModel, Field

from servers.shared.models import (
    BaseLogStepRequest,
    BaseLogStepResponse,
    BaseAskQuestionRequest,
    BaseEndSessionRequest,
    BaseEndSessionResponse,
)


# Request models
class LogStepRequest(BaseLogStepRequest):
    """FastAPI-specific request model for logging a step."""

    pass


class AskQuestionRequest(BaseAskQuestionRequest):
    """FastAPI-specific request model for asking a question."""

    pass


class EndSessionRequest(BaseEndSessionRequest):
    """FastAPI-specific request model for ending a session."""

    pass


# Response models
class LogStepResponse(BaseLogStepResponse):
    """FastAPI-specific response model for log step endpoint."""

    pass


# FastAPI-specific: Response only contains question ID (non-blocking)
class AskQuestionResponse(BaseModel):
    """FastAPI-specific response model for ask question endpoint."""

    question_id: str = Field(..., description="ID of the created question")


class EndSessionResponse(BaseEndSessionResponse):
    """FastAPI-specific response model for end session endpoint."""

    pass


# FastAPI-specific: Additional model for polling question status
class QuestionStatusResponse(BaseModel):
    """Response model for question status endpoint."""

    question_id: str
    status: str = Field(
        ..., description="Status of the question: 'pending' or 'answered'"
    )
    answer: Optional[str] = Field(
        None, description="Answer text if status is 'answered'"
    )
    asked_at: str
    answered_at: Optional[str] = None
