from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from shared.database.models import User
from shared.database.session import get_db
from sqlalchemy.orm import Session

from ..auth.dependencies import get_current_user
from ..db import submit_answer
from ..models import AnswerRequest

router = APIRouter(prefix="/questions", tags=["questions"])


@router.post("/{question_id}/answer")
async def answer_question(
    question_id: UUID,
    request: AnswerRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Submit an answer to a pending question for the current user"""
    result = submit_answer(db, question_id, request.answer, current_user.id)
    if not result:
        raise HTTPException(
            status_code=404, detail="Question not found or already answered"
        )
    return {"success": True, "message": "Answer submitted successfully"}
