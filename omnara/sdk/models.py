"""Data models for the Omnara SDK."""

from typing import List, Optional
from dataclasses import dataclass


@dataclass
class LogStepResponse:
    """Response from logging a step."""

    success: bool
    agent_instance_id: str
    step_number: int
    user_feedback: List[str]


@dataclass
class QuestionResponse:
    """Response from asking a question."""

    answer: str
    question_id: str


@dataclass
class QuestionStatus:
    """Status of a question."""

    question_id: str
    status: str  # 'pending' or 'answered'
    answer: Optional[str]
    asked_at: str
    answered_at: Optional[str]


@dataclass
class EndSessionResponse:
    """Response from ending a session."""

    success: bool
    agent_instance_id: str
    final_status: str
