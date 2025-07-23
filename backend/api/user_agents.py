"""
User Agent API endpoints for managing user-specific agent configurations.
"""

from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from shared.database.models import User, UserAgent
from shared.database.session import get_db
from sqlalchemy.orm import Session
from sqlalchemy import and_

from ..auth.dependencies import get_current_user
from ..models import (
    UserAgentRequest,
    UserAgentResponse,
    CreateAgentInstanceRequest,
    WebhookTriggerResponse,
    AgentInstanceResponse,
)
from ..db import (
    create_user_agent,
    get_user_agents,
    update_user_agent,
    delete_user_agent,
    trigger_webhook_agent,
    get_user_agent_instances,
)

router = APIRouter(tags=["user-agents"])


@router.get("/user-agents", response_model=list[UserAgentResponse])
async def list_user_agents(
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get all user agents for the current user"""
    agents = get_user_agents(db, current_user.id)
    return agents


@router.post("/user-agents", response_model=UserAgentResponse)
async def create_new_user_agent(
    request: UserAgentRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Create a new user agent configuration"""
    agent = create_user_agent(db, current_user.id, request)
    if not agent:
        raise HTTPException(
            status_code=400, detail=f"An agent named '{request.name}' already exists"
        )
    return agent


@router.patch("/user-agents/{agent_id}", response_model=UserAgentResponse)
async def update_existing_user_agent(
    agent_id: UUID,
    request: UserAgentRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Update an existing user agent configuration"""
    agent = update_user_agent(db, agent_id, current_user.id, request)
    if not agent:
        raise HTTPException(status_code=404, detail="User agent not found")
    return agent


@router.delete("/user-agents/{agent_id}")
async def delete_existing_user_agent(
    agent_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Delete an existing user agent and all its instances"""
    success = delete_user_agent(db, agent_id, current_user.id)
    if not success:
        raise HTTPException(status_code=404, detail="User agent not found")
    return {"message": "User agent and all associated instances deleted successfully"}


@router.get(
    "/user-agents/{agent_id}/instances", response_model=list[AgentInstanceResponse]
)
async def get_user_agent_instances_list(
    agent_id: UUID,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Get all instances for a specific user agent"""
    instances = get_user_agent_instances(db, agent_id, current_user.id)
    if instances is None:
        raise HTTPException(status_code=404, detail="User agent not found")
    return instances


@router.post("/user-agents/{agent_id}/instances", response_model=WebhookTriggerResponse)
async def create_agent_instance(
    agent_id: UUID,
    request: CreateAgentInstanceRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Create a new instance of a user agent (trigger webhook if applicable)"""

    # Get the user agent
    user_agent = (
        db.query(UserAgent)
        .filter(and_(UserAgent.id == agent_id, UserAgent.user_id == current_user.id))
        .first()
    )

    if not user_agent:
        raise HTTPException(status_code=404, detail="User agent not found")

    # Check if this agent has a webhook configured
    if not user_agent.webhook_url:
        raise HTTPException(
            status_code=400, detail="Webhook URL is required to create agent instances"
        )

    # Trigger the webhook
    result = await trigger_webhook_agent(
        db,
        user_agent,
        current_user.id,
        request.prompt,
        request.name,
        request.worktree_name,
    )

    # Handle different failure cases with appropriate HTTP status codes
    if not result.success:
        if "Agent limit exceeded" in result.message:
            # 402 Payment Required for quota exceeded
            raise HTTPException(status_code=402, detail=result.error or result.message)
        elif "Failed to trigger webhook" in result.message:
            # 502 Bad Gateway for webhook communication failure
            raise HTTPException(status_code=502, detail=result.error or result.message)
        else:
            # 500 for other unexpected errors
            raise HTTPException(status_code=500, detail=result.error or result.message)

    return result
