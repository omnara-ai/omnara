"""API routes for agent operations."""

from typing import Annotated
from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException, status

from shared.database.session import get_db
from shared.database import Message, SenderType
from servers.shared.db import (
    send_agent_message,
    end_session,
    get_agent_instance,
    validate_agent_access,
    get_queued_user_messages,
)
from servers.shared.notification_utils import send_message_notifications
from .auth import get_current_user_id
from .models import (
    CreateMessageRequest,
    CreateMessageResponse,
    EndSessionRequest,
    EndSessionResponse,
    GetMessagesResponse,
    MessageResponse,
)

agent_router = APIRouter(tags=["agents"])


@agent_router.post("/messages", response_model=CreateMessageResponse)
async def create_message(
    request: CreateMessageRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> CreateMessageResponse:
    """Create a new agent message (step or question).

    This endpoint:
    - Creates or retrieves an agent instance
    - Creates a new message (step or question based on requires_user_input)
    - Returns the message ID and any queued user messages
    - Sends notifications if requested
    """
    db = next(get_db())

    try:
        # Use the unified send_agent_message function
        instance_id, queued_messages = await send_agent_message(
            db=db,
            agent_type=request.agent_type,
            content=request.content,
            user_id=user_id,
            agent_instance_id=request.agent_instance_id,
            requires_user_input=request.requires_user_input,
            git_diff=request.git_diff,
        )
        
        # Send notifications if requested
        if request.send_email is not None or request.send_sms is not None or request.send_push is not None:
            await send_message_notifications(
                db=db,
                instance_id=UUID(instance_id),
                content=request.content,
                requires_user_input=request.requires_user_input,
                send_email=request.send_email,
                send_sms=request.send_sms,
                send_push=request.send_push,
            )
        
        # Commit the transaction
        db.commit()

        return CreateMessageResponse(
            success=True,
            agent_instance_id=instance_id,
            queued_user_messages=queued_messages,
        )
    except ValueError as e:
        db.rollback()
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()


@agent_router.get("/messages/pending", response_model=GetMessagesResponse)
async def get_pending_messages(
    agent_instance_id: str,
    last_read_message_id: str | None,
    user_id: Annotated[str, Depends(get_current_user_id)],
) -> GetMessagesResponse:
    """Get pending user messages for an agent instance.

    This endpoint:
    - Returns all user messages since the provided last_read_message_id
    - Updates the last_read_message_id to the latest message
    - Returns None status if another process has already read the messages
    """
    db = next(get_db())

    try:
        # Validate access
        instance = validate_agent_access(db, agent_instance_id, user_id)
        
        # Parse last_read_message_id if provided
        last_read_uuid = UUID(last_read_message_id) if last_read_message_id else None
        
        # Get queued messages
        messages = get_queued_user_messages(db, instance.id, last_read_uuid)
        
        # If messages is None, another process has read the messages
        if messages is None:
            return GetMessagesResponse(
                agent_instance_id=agent_instance_id,
                messages=[],
                status="stale",  # Indicate that the last_read_message_id is stale
            )
        
        # Commit the transaction to persist the updated last_read_message_id
        db.commit()
        
        # Convert to response format
        message_responses = [
            MessageResponse(
                id=str(msg.id),
                content=msg.content,
                sender_type=msg.sender_type.value,
                created_at=msg.created_at.isoformat(),
                requires_user_input=msg.requires_user_input,
            )
            for msg in messages
        ]
        
        return GetMessagesResponse(
            agent_instance_id=agent_instance_id,
            messages=message_responses,
            status="ok",
        )
    except ValueError as e:
        db.rollback()
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()


@agent_router.post("/sessions/end", response_model=EndSessionResponse)
async def end_session_endpoint(
    request: EndSessionRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> EndSessionResponse:
    """End an agent session and mark it as completed.

    This endpoint:
    - Marks the agent instance as COMPLETED
    - Sets the session end time
    """
    db = next(get_db())

    try:
        # Use the end_session function from queries
        instance_id, final_status = end_session(
            db=db,
            agent_instance_id=request.agent_instance_id,
            user_id=user_id,
        )
        
        # Commit the transaction
        db.commit()

        return EndSessionResponse(
            success=True,
            agent_instance_id=instance_id,
            final_status=final_status,
        )
    except ValueError as e:
        db.rollback()
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail=f"Internal server error: {str(e)}",
        )
    finally:
        db.close()
