from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from shared.database.models import User
from shared.database.session import get_db
from shared.database.enums import AgentStatus
from sqlalchemy.orm import Session

from ..auth.dependencies import get_current_user
from ..db import (
    get_agent_instance_detail,
    get_agent_type_instances,
    get_agent_summary,
    get_all_agent_instances,
    get_all_agent_types_with_instances,
    mark_instance_completed,
    submit_user_feedback,
)
from ..models import (
    AgentInstanceDetail,
    AgentInstanceResponse,
    AgentTypeOverview,
    UserFeedbackRequest,
    UserFeedbackResponse,
)

router = APIRouter(tags=["agents"])


@router.get("/agent-types", response_model=list[AgentTypeOverview])
async def list_agent_types(
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get all agent types with their instances for the current user"""
    agent_types = get_all_agent_types_with_instances(db, current_user.id)
    return agent_types


@router.get("/agent-instances", response_model=list[AgentInstanceResponse])
async def list_all_agent_instances(
    limit: int | None = None,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get all agent instances for the current user"""
    instances = get_all_agent_instances(db, current_user.id, limit=limit)
    return instances


@router.get("/agent-summary")
async def get_all_agent_summary(
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get lightweight summary of agent counts for dashboard KPIs"""
    summary = get_agent_summary(db, current_user.id)
    return summary


@router.get(
    "/agent-types/{type_id}/instances", response_model=list[AgentInstanceResponse]
)
async def get_type_instances(
    type_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get all instances for a specific agent type for the current user"""
    result = get_agent_type_instances(db, type_id, current_user.id)
    if result is None:
        raise HTTPException(status_code=404, detail="Agent type not found")
    return result


@router.get("/agent-instances/{instance_id}", response_model=AgentInstanceDetail)
async def get_instance_detail(
    instance_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get detailed information about a specific agent instance for the current user"""
    result = get_agent_instance_detail(db, instance_id, current_user.id)
    if not result:
        raise HTTPException(status_code=404, detail="Agent instance not found")
    return result


@router.post(
    "/agent-instances/{instance_id}/feedback", response_model=UserFeedbackResponse
)
async def add_user_feedback(
    instance_id: UUID,
    request: UserFeedbackRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Submit user feedback for an agent instance for the current user"""
    result = submit_user_feedback(db, instance_id, request.feedback, current_user.id)
    if not result:
        raise HTTPException(status_code=404, detail="Agent instance not found")
    return result


@router.put(
    "/agent-instances/{instance_id}/status",
    response_model=AgentInstanceResponse,
)
async def update_agent_status(
    instance_id: UUID,
    status_update: dict,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Update an agent instance status for the current user"""
    # For now, we only support marking as completed
    if status_update.get("status") == AgentStatus.COMPLETED:
        result = mark_instance_completed(db, instance_id, current_user.id)
        if not result:
            raise HTTPException(status_code=404, detail="Agent instance not found")
        return result
    else:
        raise HTTPException(status_code=400, detail="Status update not supported")
