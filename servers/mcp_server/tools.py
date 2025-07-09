"""Shared MCP Tools for Agent Dashboard

This module contains the core tool implementations that are shared between
the hosted server and stdio server. The authentication logic is handled
by the individual servers.
"""

from fastmcp import Context
from shared.database.session import get_db

from servers.shared.db import wait_for_answer
from servers.shared.core import (
    process_log_step,
    create_agent_question,
    process_end_session,
)
from .models import AskQuestionResponse, EndSessionResponse, LogStepResponse


def log_step_impl(
    agent_instance_id: str | None = None,
    agent_type: str = "",
    step_description: str = "",
    user_id: str = "",
) -> LogStepResponse:
    """Core implementation of the log_step tool.

    Args:
        agent_instance_id: Existing agent instance ID (optional)
        agent_type: Name of the agent (e.g., 'Claude Code', 'Cursor')
        step_description: High-level description of the current step
        user_id: Authenticated user ID

    Returns:
        LogStepResponse with success status, instance details, and user feedback
    """
    # Validate inputs
    if not agent_type:
        raise ValueError("agent_type is required")
    if not step_description:
        raise ValueError("step_description is required")
    if not user_id:
        raise ValueError("user_id is required")

    # Get database session
    db = next(get_db())

    try:
        # Use shared business logic
        instance_id, step_number, user_feedback = process_log_step(
            db=db,
            agent_type=agent_type,
            step_description=step_description,
            user_id=user_id,
            agent_instance_id=agent_instance_id,
        )

        return LogStepResponse(
            success=True,
            agent_instance_id=instance_id,
            step_number=step_number,
            user_feedback=user_feedback,
        )

    except Exception:
        db.rollback()
        raise
    finally:
        db.close()


async def ask_question_impl(
    agent_instance_id: str | None = None,
    question_text: str | None = None,
    user_id: str = "",
    tool_context: Context | None = None,
) -> AskQuestionResponse:
    """Core implementation of the ask_question tool.

    Args:
        agent_instance_id: Agent instance ID
        question_text: Question to ask the user
        user_id: Authenticated user ID
        tool_context: MCP context for progress reporting

    Returns:
        AskQuestionResponse with the user's answer
    """
    # Validate inputs
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not question_text:
        raise ValueError("question_text is required")
    if not user_id:
        raise ValueError("user_id is required")

    # Get database session
    db = next(get_db())

    try:
        # Use shared business logic to create question
        question = create_agent_question(
            db=db,
            agent_instance_id=agent_instance_id,
            question_text=question_text,
            user_id=user_id,
        )

        # MCP-specific: Wait for answer (blocking)
        answer = await wait_for_answer(db, question.id, tool_context=tool_context)

        if answer is None:
            raise TimeoutError("Question timed out waiting for user response")

        return AskQuestionResponse(answer=answer, question_id=str(question.id))

    except Exception:
        db.rollback()
        raise
    finally:
        db.close()


def end_session_impl(
    agent_instance_id: str,
    user_id: str = "",
) -> EndSessionResponse:
    """Core implementation of the end_session tool.

    Args:
        agent_instance_id: Agent instance ID to end
        user_id: Authenticated user ID

    Returns:
        EndSessionResponse with success status and final session details
    """
    # Validate inputs
    if not user_id:
        raise ValueError("user_id is required")

    # Get database session
    db = next(get_db())

    try:
        # Use shared business logic
        instance_id, final_status = process_end_session(
            db=db,
            agent_instance_id=agent_instance_id,
            user_id=user_id,
        )

        return EndSessionResponse(
            success=True,
            agent_instance_id=instance_id,
            final_status=final_status,
        )

    except Exception:
        db.rollback()
        raise
    finally:
        db.close()
