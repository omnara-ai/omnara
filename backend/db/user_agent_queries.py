"""
Database queries for UserAgent operations.
"""

import httpx
from datetime import datetime, timezone
from uuid import UUID
import hashlib

from shared.database import (
    UserAgent,
    AgentInstance,
    AgentStatus,
    APIKey,
    AgentStep,
    AgentQuestion,
    AgentUserFeedback,
)
from sqlalchemy import and_, func
from sqlalchemy.orm import Session, joinedload

from ..models import UserAgentRequest, WebhookTriggerResponse
from .queries import _format_instance
from ..auth.jwt_utils import create_api_key_jwt


def create_user_agent(
    db: Session, user_id: UUID, request: UserAgentRequest
) -> dict | None:
    """Create a new user agent configuration"""

    # Check if agent with same name already exists for this user
    existing = (
        db.query(UserAgent)
        .filter(and_(UserAgent.user_id == user_id, UserAgent.name == request.name))
        .first()
    )

    if existing:
        return None

    user_agent = UserAgent(
        user_id=user_id,
        name=request.name,
        webhook_url=request.webhook_url,
        webhook_api_key=request.webhook_api_key,
        is_active=request.is_active,
    )

    db.add(user_agent)
    db.commit()
    db.refresh(user_agent)

    return _format_user_agent(user_agent, db)


def get_user_agents(db: Session, user_id: UUID) -> list[dict]:
    """Get all user agents for a specific user"""

    user_agents = db.query(UserAgent).filter(UserAgent.user_id == user_id).all()

    return [_format_user_agent(agent, db) for agent in user_agents]


def update_user_agent(
    db: Session, agent_id: UUID, user_id: UUID, request: UserAgentRequest
) -> dict | None:
    """Update an existing user agent configuration"""

    user_agent = (
        db.query(UserAgent)
        .filter(and_(UserAgent.id == agent_id, UserAgent.user_id == user_id))
        .first()
    )

    if not user_agent:
        return None

    user_agent.name = request.name
    user_agent.webhook_url = request.webhook_url
    user_agent.webhook_api_key = request.webhook_api_key
    user_agent.is_active = request.is_active
    user_agent.updated_at = datetime.now(timezone.utc)

    db.commit()
    db.refresh(user_agent)

    return _format_user_agent(user_agent, db)


async def trigger_webhook_agent(
    db: Session,
    user_agent: UserAgent,
    user_id: UUID,
    prompt: str,
    name: str | None = None,
    worktree_name: str | None = None,
) -> WebhookTriggerResponse:
    """Trigger a webhook agent by calling the webhook URL"""

    if not user_agent.webhook_url:
        return WebhookTriggerResponse(
            success=False,
            message="Webhook URL not configured",
            error="No webhook URL found for this agent",
        )

    # Create the agent instance first
    instance = AgentInstance(
        user_agent_id=user_agent.id, user_id=user_id, status=AgentStatus.ACTIVE
    )
    db.add(instance)
    db.commit()
    db.refresh(instance)

    # Get or create an Omnara API key for this agent
    api_key_name = f"{user_agent.name} Key"

    # Check if an API key already exists for this agent
    existing_key = (
        db.query(APIKey)
        .filter(
            and_(
                APIKey.user_id == user_id,
                APIKey.name == api_key_name,
                APIKey.is_active,
            )
        )
        .first()
    )

    if existing_key:
        omnara_api_key = existing_key.api_key
    else:
        # Create a new API key
        jwt_token = create_api_key_jwt(
            user_id=str(user_id),
            expires_in_days=None,  # No expiration
        )

        # Store API key in database
        api_key = APIKey(
            user_id=user_id,
            name=api_key_name,
            api_key_hash=hashlib.sha256(jwt_token.encode()).hexdigest(),
            api_key=jwt_token,
            expires_at=None,  # No expiration
        )
        db.add(api_key)
        db.commit()

        omnara_api_key = jwt_token

    # Prepare webhook payload
    payload = {
        "agent_instance_id": str(instance.id),
        "prompt": prompt,
    }

    # Add optional fields if provided
    if name is not None:
        payload["name"] = name
    if worktree_name is not None:
        payload["worktree_name"] = worktree_name

    # Call the webhook
    try:
        async with httpx.AsyncClient(timeout=30.0) as client:
            response = await client.post(
                user_agent.webhook_url,
                json=payload,
                headers={
                    "Authorization": f"Bearer {user_agent.webhook_api_key}"
                    if user_agent.webhook_api_key
                    else "",
                    "Content-Type": "application/json",
                    "X-Omnara-Api-Key": omnara_api_key,  # Add the Omnara API key header
                },
            )
            response.raise_for_status()

            return WebhookTriggerResponse(
                success=True,
                agent_instance_id=str(instance.id),
                message="Webhook triggered successfully",
            )

    except httpx.HTTPError as e:
        # Mark instance as failed
        instance.status = AgentStatus.FAILED
        instance.ended_at = datetime.now(timezone.utc)
        db.commit()

        return WebhookTriggerResponse(
            success=False,
            agent_instance_id=str(instance.id),
            message="Failed to trigger webhook",
            error=str(e),
        )
    except Exception as e:
        # Mark instance as failed
        instance.status = AgentStatus.FAILED
        instance.ended_at = datetime.now(timezone.utc)
        db.commit()

        return WebhookTriggerResponse(
            success=False,
            agent_instance_id=str(instance.id),
            message="Unexpected error occurred",
            error=str(e),
        )


def get_user_agent_instances(db: Session, agent_id: UUID, user_id: UUID) -> list | None:
    """Get all instances for a specific user agent"""

    # Verify the user agent exists and belongs to the user
    user_agent = (
        db.query(UserAgent)
        .filter(and_(UserAgent.id == agent_id, UserAgent.user_id == user_id))
        .first()
    )

    if not user_agent:
        return None

    # Get all instances for this user agent with relationships loaded
    instances = (
        db.query(AgentInstance)
        .options(
            joinedload(AgentInstance.questions),
            joinedload(AgentInstance.steps),
            joinedload(AgentInstance.user_feedback),
            joinedload(AgentInstance.user_agent),
        )
        .filter(AgentInstance.user_agent_id == agent_id)
        .order_by(AgentInstance.started_at.desc())
        .all()
    )

    # Format instances using the same helper function used by other endpoints
    return [_format_instance(instance) for instance in instances]


def delete_user_agent(db: Session, agent_id: UUID, user_id: UUID) -> bool:
    """Delete a user agent and all its associated instances and related data"""

    # First verify the user agent exists and belongs to the user
    user_agent = (
        db.query(UserAgent)
        .filter(and_(UserAgent.id == agent_id, UserAgent.user_id == user_id))
        .first()
    )

    if not user_agent:
        return False

    # Get all agent instances for this user agent
    agent_instances = (
        db.query(AgentInstance).filter(AgentInstance.user_agent_id == agent_id).all()
    )

    # For each agent instance, delete all related data
    for instance in agent_instances:
        # Delete agent steps
        db.query(AgentStep).filter(AgentStep.agent_instance_id == instance.id).delete()

        # Delete agent questions
        db.query(AgentQuestion).filter(
            AgentQuestion.agent_instance_id == instance.id
        ).delete()

        # Delete user feedback
        db.query(AgentUserFeedback).filter(
            AgentUserFeedback.agent_instance_id == instance.id
        ).delete()

    # Delete all agent instances
    db.query(AgentInstance).filter(AgentInstance.user_agent_id == agent_id).delete()

    # Delete the user agent
    db.delete(user_agent)
    db.commit()

    return True


def _format_user_agent(user_agent: UserAgent, db: Session) -> dict:
    """Helper function to format a user agent with instance counts"""

    # Get instance counts
    instance_count = (
        db.query(func.count(AgentInstance.id))
        .filter(AgentInstance.user_agent_id == user_agent.id)
        .scalar()
    )

    active_instance_count = (
        db.query(func.count(AgentInstance.id))
        .filter(
            and_(
                AgentInstance.user_agent_id == user_agent.id,
                AgentInstance.status == AgentStatus.ACTIVE,
            )
        )
        .scalar()
    )

    waiting_instance_count = (
        db.query(func.count(AgentInstance.id))
        .filter(
            and_(
                AgentInstance.user_agent_id == user_agent.id,
                AgentInstance.status == AgentStatus.AWAITING_INPUT,
            )
        )
        .scalar()
    )

    completed_instance_count = (
        db.query(func.count(AgentInstance.id))
        .filter(
            and_(
                AgentInstance.user_agent_id == user_agent.id,
                AgentInstance.status == AgentStatus.COMPLETED,
            )
        )
        .scalar()
    )

    error_instance_count = (
        db.query(func.count(AgentInstance.id))
        .filter(
            and_(
                AgentInstance.user_agent_id == user_agent.id,
                AgentInstance.status.in_([AgentStatus.FAILED, AgentStatus.KILLED]),
            )
        )
        .scalar()
    )

    return {
        "id": str(user_agent.id),
        "name": user_agent.name,
        "webhook_url": user_agent.webhook_url,
        "is_active": user_agent.is_active,
        "created_at": user_agent.created_at,
        "updated_at": user_agent.updated_at,
        "instance_count": instance_count or 0,
        "active_instance_count": active_instance_count or 0,
        "waiting_instance_count": waiting_instance_count or 0,
        "completed_instance_count": completed_instance_count or 0,
        "error_instance_count": error_instance_count or 0,
        "has_webhook": bool(user_agent.webhook_url),
    }
