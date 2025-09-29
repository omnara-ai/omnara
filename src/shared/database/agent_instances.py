"""Shared helpers for creating agent instances."""

from __future__ import annotations

from typing import Optional
from uuid import UUID, uuid4

from sqlalchemy.orm import Session

from .billing_operations import check_agent_limit
from .models import AgentInstance, UserAgent


def _normalize_agent_name(name: str) -> str:
    return name.strip().lower()


def get_or_create_user_agent(
    db: Session,
    user_id: UUID,
    agent_name: str,
) -> UserAgent:
    """Fetch an existing user agent or create a new one for the given user."""

    normalized_name = _normalize_agent_name(agent_name)
    user_agent = (
        db.query(UserAgent)
        .filter(
            UserAgent.user_id == user_id,
            UserAgent.name == normalized_name,
            UserAgent.is_deleted.is_(False),
        )
        .first()
    )
    if user_agent:
        return user_agent

    user_agent = UserAgent(
        user_id=user_id,
        name=normalized_name,
        is_active=True,
        is_deleted=False,
    )
    db.add(user_agent)
    db.flush()
    return user_agent


def create_agent_instance(
    db: Session,
    user_id: UUID,
    *,
    agent_name: str,
    instance_id: Optional[UUID] = None,
    name: Optional[str] = None,
    instance_metadata: Optional[dict] = None,
) -> AgentInstance:
    """Create and persist an agent instance with optional relay metadata."""

    check_agent_limit(user_id, db, increment=1)

    user_agent = get_or_create_user_agent(db, user_id, agent_name)
    final_id = instance_id or uuid4()

    metadata_payload = None
    if instance_metadata:
        metadata_payload = dict(instance_metadata)

    instance = AgentInstance(
        id=final_id,
        user_id=user_id,
        user_agent_id=user_agent.id,
        name=name,
        instance_metadata=metadata_payload,
    )
    db.add(instance)
    db.flush()
    return instance
