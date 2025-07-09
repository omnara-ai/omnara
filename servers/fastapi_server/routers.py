"""API routes for agent operations."""

from typing import Annotated

from fastapi import APIRouter, Depends, HTTPException, status

from shared.database.session import get_db
from servers.shared.db import get_question, get_agent_instance
from servers.shared.core import (
    process_log_step,
    create_agent_question,
    process_end_session,
)
from .auth import get_current_user_id
from .models import (
    AskQuestionRequest,
    AskQuestionResponse,
    EndSessionRequest,
    EndSessionResponse,
    LogStepRequest,
    LogStepResponse,
    QuestionStatusResponse,
)

agent_router = APIRouter(tags=["agents"])


@agent_router.post("/steps", response_model=LogStepResponse)
async def log_step(
    request: LogStepRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> LogStepResponse:
    """Log a high-level step the agent is performing.

    This endpoint:
    - Creates or retrieves an agent instance
    - Logs the step with a sequential number
    - Returns any unretrieved user feedback

    User feedback is automatically marked as retrieved.
    """
    db = next(get_db())

    try:
        # Use shared business logic
        instance_id, step_number, user_feedback = process_log_step(
            db=db,
            agent_type=request.agent_type,
            step_description=request.step_description,
            user_id=user_id,
            agent_instance_id=request.agent_instance_id,
        )

        return LogStepResponse(
            success=True,
            agent_instance_id=instance_id,
            step_number=step_number,
            user_feedback=user_feedback,
        )
    except ValueError as e:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()


@agent_router.post("/questions", response_model=AskQuestionResponse)
def ask_question(
    request: AskQuestionRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> AskQuestionResponse:
    """Create a question for the user to answer.

    This endpoint:
    - Creates a question record in the database
    - Returns immediately with the question ID
    - Client should poll GET /questions/{question_id} for the answer

    Note: This endpoint is non-blocking.
    """
    db = next(get_db())

    try:
        # Use shared business logic to create question
        question = create_agent_question(
            db=db,
            agent_instance_id=request.agent_instance_id,
            question_text=request.question_text,
            user_id=user_id,
        )
        db.commit()

        # FastAPI-specific: Return immediately with question ID (non-blocking)
        return AskQuestionResponse(
            question_id=str(question.id),
        )
    except HTTPException:
        raise
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()


@agent_router.get("/questions/{question_id}", response_model=QuestionStatusResponse)
async def get_question_status(
    question_id: str, user_id: Annotated[str, Depends(get_current_user_id)]
) -> QuestionStatusResponse:
    """Get the status of a question.

    This endpoint allows polling for question answers without blocking.
    Returns the current status and answer (if available).
    """
    db = next(get_db())

    try:
        # Get the question
        question = get_question(db, question_id)
        if not question:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND, detail="Question not found"
            )

        # Verify the question belongs to the authenticated user
        instance = get_agent_instance(db, str(question.agent_instance_id))
        if not instance or str(instance.user_id) != user_id:
            raise HTTPException(
                status_code=status.HTTP_403_FORBIDDEN, detail="Access denied"
            )

        # Return question status
        return QuestionStatusResponse(
            question_id=str(question.id),
            status="answered" if question.answer_text else "pending",
            answer=question.answer_text,
            asked_at=question.asked_at.isoformat(),
            answered_at=question.answered_at.isoformat()
            if question.answered_at
            else None,
        )
    finally:
        db.close()


@agent_router.post("/sessions/end", response_model=EndSessionResponse)
async def end_session(
    request: EndSessionRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> EndSessionResponse:
    """End an agent session and mark it as completed.

    This endpoint:
    - Marks the agent instance as COMPLETED
    - Sets the session end time
    - Deactivates any pending questions
    """
    db = next(get_db())

    try:
        # Use shared business logic
        instance_id, final_status = process_end_session(
            db=db,
            agent_instance_id=request.agent_instance_id,
            user_id=user_id,
        )

        return EndSessionResponse(
            success=True,
            agent_instance_id=instance_id,
            final_status=final_status,
        )
    except ValueError as e:
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()
