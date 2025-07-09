"""
Database queries for UserAgent operations.
"""

import httpx
from datetime import datetime, timezone
from uuid import UUID

from shared.database import UserAgent, AgentInstance, AgentStatus
from sqlalchemy import and_, func
from sqlalchemy.orm import Session, joinedload

from ..models import UserAgentRequest, WebhookTriggerResponse


def create_user_agent(db: Session, user_id: UUID, request: UserAgentRequest) -> dict:
    """Create a new user agent configuration"""

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
    db: Session, user_agent: UserAgent, user_id: UUID, prompt: str
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

    # Prepare webhook payload
    payload = {
        "agent_instance_id": str(instance.id),
        "prompt": prompt,
        "omnara_api_key": user_agent.webhook_api_key,
        "omnara_tools": {
            "log_step": {
                "description": "Log a step in the agent's execution",
                "endpoint": "/api/v1/mcp/tools/log_step",
            },
            "ask_question": {
                "description": "Ask a question to the user",
                "endpoint": "/api/v1/mcp/tools/ask_question",
            },
            "end_session": {
                "description": "End the agent session",
                "endpoint": "/api/v1/mcp/tools/end_session",
            },
        },
    }

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
        )
        .filter(AgentInstance.user_agent_id == agent_id)
        .order_by(AgentInstance.started_at.desc())
        .all()
    )

    # Format instances similar to how agent-type instances are formatted
    return [
        {
            "id": str(instance.id),
            "user_agent_id": str(instance.user_agent_id),
            "user_id": str(instance.user_id),
            "status": instance.status.value,
            "started_at": instance.started_at,
            "ended_at": instance.ended_at,
            "pending_questions_count": len(
                [q for q in instance.questions if q.is_active and not q.answer_text]
            ),
            "steps_count": len(instance.steps),
            "user_feedback_count": len(instance.user_feedback),
            "last_signal_at": instance.steps[-1].created_at
            if instance.steps
            else instance.started_at,
        }
        for instance in instances
    ]


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
