"""Shared MCP Tools for Agent Dashboard

This module contains the core tool implementations that are shared between
the hosted server and stdio server. The authentication logic is handled
by the individual servers.
"""

import uuid
from uuid import UUID

from fastmcp import Context
from shared.database.session import get_db

from servers.shared.db import (
    send_agent_message,
    end_session,
    wait_for_answer,
    create_agent_message,
    get_or_create_agent_instance,
)
from servers.shared.db.queries import (
    get_queue_status,
    get_queue_for_instance,
)
from .models import AskQuestionResponse, EndSessionResponse, LogStepResponse


async def log_step_impl(
    agent_instance_id: str | None = None,
    agent_type: str = "",
    step_description: str = "",
    user_id: str = "",
) -> LogStepResponse:
    """Core implementation of the log_step tool.

    Args:
        agent_instance_id: Existing agent instance ID (optional)
        agent_type: Name of the agent (e.g., 'claude_code', 'cursor')
        step_description: High-level description of the current step
        user_id: Authenticated user ID

    Returns:
        LogStepResponse with success status, instance details, and user feedback
    """
    # Generate a new UUID if agent_instance_id is not provided
    if not agent_instance_id:
        agent_instance_id = str(uuid.uuid4())
    else:
        # Validate the provided UUID
        try:
            UUID(agent_instance_id)
        except ValueError:
            raise ValueError(
                f"Invalid agent_instance_id format: must be a valid UUID, got '{agent_instance_id}'"
            )

    if not agent_type:
        raise ValueError("agent_type is required")
    if not step_description:
        raise ValueError("step_description is required")
    if not user_id:
        raise ValueError("user_id is required")

    db = next(get_db())

    try:
        # Use send_agent_message for steps (requires_user_input=False)
        instance_id, message_id, queued_messages = await send_agent_message(
            db=db,
            agent_instance_id=agent_instance_id,
            content=step_description,
            user_id=user_id,
            agent_type=agent_type,
            requires_user_input=False,
        )

        # For backward compatibility, we need to return a step number
        # Count the number of agent messages (steps) for this instance
        from shared.database import Message, SenderType

        step_count = (
            db.query(Message)
            .filter(
                Message.agent_instance_id == UUID(instance_id),
                Message.sender_type == SenderType.AGENT,
                Message.requires_user_input.is_(False),
            )
            .count()
        )

        db.commit()

        return LogStepResponse(
            success=True,
            agent_instance_id=instance_id,
            step_number=step_count,
            user_feedback=[msg.content for msg in queued_messages],
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
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not question_text:
        raise ValueError("question_text is required")
    if not user_id:
        raise ValueError("user_id is required")
    try:
        UUID(agent_instance_id)
    except ValueError:
        raise ValueError(
            f"Invalid agent_instance_id format: must be a valid UUID, got '{agent_instance_id}'"
        )

    db = next(get_db())

    try:
        # Validate access first (agent_instance_id is required here)
        instance = get_or_create_agent_instance(db, agent_instance_id, user_id)

        # Create question message (requires_user_input=True)
        question = create_agent_message(
            db=db,
            instance_id=instance.id,
            content=question_text,
            requires_user_input=True,
        )

        # Commit to make the question visible
        db.commit()

        # Wait for answer
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
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not user_id:
        raise ValueError("user_id is required")
    try:
        UUID(agent_instance_id)
    except ValueError:
        raise ValueError(
            f"Invalid agent_instance_id format: must be a valid UUID, got '{agent_instance_id}'"
        )

    db = next(get_db())

    try:
        instance_id, final_status = end_session(
            db=db,
            agent_instance_id=agent_instance_id,
            user_id=user_id,
        )

        # Commit the transaction
        db.commit()

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


def check_prompt_queue_impl(
    agent_instance_id: str,
    user_id: str = "",
) -> str:
    """Core implementation of the check_prompt_queue tool.

    Args:
        agent_instance_id: Agent instance ID to check
        user_id: Authenticated user ID

    Returns:
        String with queue status information
    """
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not user_id:
        raise ValueError("user_id is required")
    try:
        UUID(agent_instance_id)
    except ValueError:
        raise ValueError(
            f"Invalid agent_instance_id format: must be a valid UUID, got '{agent_instance_id}'"
        )

    db = next(get_db())

    try:
        status = get_queue_status(
            db=db,
            agent_instance_id=UUID(agent_instance_id),
            user_id=UUID(user_id),
        )

        if status["pending"] == 0:
            return "No prompts queued."

        next_prompt = None
        if status["next_position"] is not None:
            from shared.database import PromptQueue, PromptQueueStatus

            next_prompt_obj = (
                db.query(PromptQueue)
                .filter(
                    PromptQueue.agent_instance_id == UUID(agent_instance_id),
                    PromptQueue.status == PromptQueueStatus.PENDING,
                    PromptQueue.position == status["next_position"],
                )
                .first()
            )
            if next_prompt_obj:
                # Truncate long prompts
                next_prompt = (
                    next_prompt_obj.prompt_text[:100] + "..."
                    if len(next_prompt_obj.prompt_text) > 100
                    else next_prompt_obj.prompt_text
                )

        result = f"Queue status: {status['pending']} pending, {status['sent']} sent, {status['failed']} failed."
        if next_prompt:
            result += f"\nNext prompt: {next_prompt}"

        return result

    except ValueError as e:
        return f"Error: {str(e)}"
    except Exception as e:
        return f"Error checking queue: {str(e)}"
    finally:
        db.close()


def view_prompt_queue_impl(
    agent_instance_id: str,
    user_id: str = "",
) -> str:
    """Core implementation of the view_prompt_queue tool.

    Args:
        agent_instance_id: Agent instance ID to check
        user_id: Authenticated user ID

    Returns:
        String with formatted list of queued prompts
    """
    if not agent_instance_id:
        raise ValueError("agent_instance_id is required")
    if not user_id:
        raise ValueError("user_id is required")
    try:
        UUID(agent_instance_id)
    except ValueError:
        raise ValueError(
            f"Invalid agent_instance_id format: must be a valid UUID, got '{agent_instance_id}'"
        )

    db = next(get_db())

    try:
        from shared.database import PromptQueueStatus

        pending_items = get_queue_for_instance(
            db=db,
            agent_instance_id=UUID(agent_instance_id),
            user_id=UUID(user_id),
            status_filter=PromptQueueStatus.PENDING,
        )

        if not pending_items:
            return "No pending prompts in queue."

        result = f"Pending prompts ({len(pending_items)}):\n"
        for i, item in enumerate(pending_items, 1):
            # Truncate long prompts for display
            prompt_preview = (
                item.prompt_text[:80] + "..."
                if len(item.prompt_text) > 80
                else item.prompt_text
            )
            result += f"{i}. {prompt_preview}\n"

        return result

    except ValueError as e:
        return f"Error: {str(e)}"
    except Exception as e:
        return f"Error viewing queue: {str(e)}"
    finally:
        db.close()
