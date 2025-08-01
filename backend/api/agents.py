from uuid import UUID
import asyncio
import json
from typing import AsyncGenerator

from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import StreamingResponse
from shared.database.models import User
from shared.database.session import get_db
from shared.database.enums import AgentStatus
from sqlalchemy.orm import Session
import asyncpg

from ..auth.dependencies import get_current_user
from ..db import (
    get_agent_instance_detail,
    get_agent_type_instances,
    get_agent_summary,
    get_all_agent_instances,
    get_all_agent_types_with_instances,
    mark_instance_completed,
    submit_user_message,
)
from ..models import (
    AgentInstanceDetail,
    AgentInstanceResponse,
    AgentTypeOverview,
    UserMessageRequest,
    UserMessageResponse,
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
    "/agent-instances/{instance_id}/messages", response_model=UserMessageResponse
)
async def create_user_message(
    instance_id: UUID,
    request: UserMessageRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Send a message to an agent instance (answers questions or provides feedback)"""
    result = submit_user_message(db, instance_id, request.content, current_user.id)
    if not result:
        raise HTTPException(status_code=404, detail="Agent instance not found")
    return result


@router.get("/agent-instances/{instance_id}/messages/stream")
async def stream_messages(
    request: Request,
    instance_id: UUID,
    token: str | None = None,
    db: Session = Depends(get_db),
):
    """Stream new messages for an agent instance using Server-Sent Events"""
    # Handle SSE authentication - token comes from query param
    if not token:
        raise HTTPException(status_code=401, detail="Token required for SSE")

    try:
        # Verify token and get user
        from ..auth.supabase_client import get_supabase_anon_client

        supabase = get_supabase_anon_client()
        user_response = supabase.auth.get_user(token)

        if not user_response or not user_response.user:
            raise HTTPException(status_code=401, detail="Invalid token")

        user_id = UUID(user_response.user.id)
    except Exception as e:
        raise HTTPException(status_code=401, detail=f"Authentication failed: {str(e)}")

    # Verify the user has access to this instance
    instance = get_agent_instance_detail(db, instance_id, user_id)
    if not instance:
        raise HTTPException(status_code=404, detail="Agent instance not found")

    async def message_generator() -> AsyncGenerator[str, None]:
        # Import settings here to avoid circular imports
        from shared.config.settings import settings

        # Create connection to PostgreSQL for LISTEN/NOTIFY
        conn = await asyncpg.connect(settings.database_url)
        try:
            # Listen to the channel for this instance
            channel_name = f"message_channel_{instance_id}"

            # Execute LISTEN command (quote channel name for UUIDs with hyphens)
            await conn.execute(f'LISTEN "{channel_name}"')

            # Create a queue to receive notifications
            notification_queue = asyncio.Queue()

            # Define callback to put notifications in queue
            def notification_callback(connection, pid, channel, payload):
                asyncio.create_task(notification_queue.put(payload))

            # Add listener with callback
            await conn.add_listener(channel_name, notification_callback)

            # Send initial connection event
            yield f"event: connected\ndata: {json.dumps({'instance_id': str(instance_id)})}\n\n"

            while True:
                # Check if client disconnected
                if await request.is_disconnected():
                    break

                try:
                    # Wait for notification with timeout for heartbeat
                    payload = await asyncio.wait_for(
                        notification_queue.get(), timeout=30.0
                    )

                    # Parse the JSON payload
                    data = json.loads(payload)

                    # Check if this is a status update or a message
                    if data.get("event_type") == "status_update":
                        # Send status_update event
                        yield f"event: status_update\ndata: {json.dumps(data)}\n\n"
                    else:
                        # Regular message event (created_at is already a string from PostgreSQL)
                        yield f"event: message\ndata: {json.dumps(data)}\n\n"

                except asyncio.TimeoutError:
                    # Send heartbeat to keep connection alive
                    yield f"event: heartbeat\ndata: {json.dumps({'timestamp': asyncio.get_event_loop().time()})}\n\n"

                except Exception as e:
                    # Send error event
                    yield f"event: error\ndata: {json.dumps({'error': str(e)})}\n\n"
                    break

        finally:
            # Clean up listener and connection
            await conn.remove_listener(channel_name, notification_callback)
            await conn.close()

    return StreamingResponse(
        message_generator(),
        media_type="text/event-stream",
        headers={
            "Cache-Control": "no-cache",
            "Connection": "keep-alive",
            "X-Accel-Buffering": "no",  # Disable Nginx buffering
        },
    )


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
