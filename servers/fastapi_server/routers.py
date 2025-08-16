"""API routes for agent operations."""

import logging
from typing import Annotated
from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException, status

from shared.database.session import get_db
from shared.database import Message, AgentInstance, SenderType, AgentStatus
from servers.shared.db import (
    send_agent_message,
    end_session,
    get_or_create_agent_instance,
    get_queued_user_messages,
    create_user_message,
)
from servers.shared.notification_utils import send_message_notifications
from .auth import get_current_user_id
from .models import (
    CreateMessageRequest,
    CreateMessageResponse,
    CreateUserMessageRequest,
    CreateUserMessageResponse,
    EndSessionRequest,
    EndSessionResponse,
    GetMessagesResponse,
    MessageResponse,
)

agent_router = APIRouter(tags=["agents"])
logger = logging.getLogger(__name__)


@agent_router.get("/auth/validate")
async def validate_api_key(
    user_id: Annotated[str, Depends(get_current_user_id)],
) -> dict:
    """Validate API key and return user info.

    This endpoint is used to verify that an API key is valid.
    Returns basic user information if authentication is successful.
    """
    return {"valid": True, "user_id": user_id}


@agent_router.post("/messages/agent", response_model=CreateMessageResponse)
async def create_agent_message_endpoint(
    request: CreateMessageRequest, user_id: Annotated[str, Depends(get_current_user_id)]
) -> CreateMessageResponse:
    """Create a new agent message.

    This endpoint:
    - Creates or retrieves an agent instance
    - Creates a new message
    - Returns the message ID and any queued user messages
    - Sends notifications if requested
    """
    db = next(get_db())

    try:
        # Use the unified send_agent_message function
        instance_id, message_id, queued_messages = await send_agent_message(
            db=db,
            agent_instance_id=request.agent_instance_id,
            content=request.content,
            user_id=user_id,
            agent_type=request.agent_type,
            requires_user_input=request.requires_user_input,
            git_diff=request.git_diff,
        )

        # Send notifications if requested
        await send_message_notifications(
            db=db,
            instance_id=UUID(instance_id),
            content=request.content,
            requires_user_input=request.requires_user_input,
            send_email=request.send_email,
            send_sms=request.send_sms,
            send_push=request.send_push,
        )

        db.commit()

        message_responses = [
            MessageResponse(
                id=str(msg.id),
                content=msg.content,
                sender_type=msg.sender_type.value,
                created_at=msg.created_at.isoformat(),
                requires_user_input=msg.requires_user_input,
            )
            for msg in queued_messages
        ]

        return CreateMessageResponse(
            success=True,
            agent_instance_id=instance_id,
            message_id=message_id,
            queued_user_messages=message_responses,
        )
    except ValueError as e:
        db.rollback()
        raise HTTPException(status_code=status.HTTP_400_BAD_REQUEST, detail=str(e))
    except HTTPException:
        db.rollback()
        raise  # Re-raise HTTPExceptions (including UsageLimitError) with their original status
    except Exception as e:
        db.rollback()
        logger.error(f"Error in create_agent_message_endpoint: {str(e)}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="Internal server error",
        )
    finally:
        db.close()


@agent_router.post("/messages/user", response_model=CreateUserMessageResponse)
async def create_user_message_endpoint(
    request: CreateUserMessageRequest,
    user_id: Annotated[str, Depends(get_current_user_id)],
) -> CreateUserMessageResponse:
    """Create a user message.

    This endpoint:
    - Creates a user message for an existing agent instance
    - Optionally marks it as read (updates last_read_message_id)
    - Returns the message ID
    """
    db = next(get_db())

    try:
        # Create the user message
        message_id, marked_as_read = create_user_message(
            db=db,
            agent_instance_id=request.agent_instance_id,
            content=request.content,
            user_id=user_id,
            mark_as_read=request.mark_as_read,
        )

        # Commit the transaction
        db.commit()

        return CreateUserMessageResponse(
            success=True,
            message_id=message_id,
            marked_as_read=marked_as_read,
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
        # Validate access (agent_instance_id is required here)
        instance = get_or_create_agent_instance(db, agent_instance_id, user_id)

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


@agent_router.patch("/messages/{message_id}/request-input")
async def request_user_input_endpoint(
    message_id: UUID,
    user_id: Annotated[str, Depends(get_current_user_id)],
) -> dict:
    """Update an agent message to request user input.

    This endpoint:
    - Updates the requires_user_input field from false to true
    - Only works on agent messages that don't already require input
    - Returns any queued user messages since this message
    - Triggers a notification via the database trigger
    """
    db = next(get_db())

    try:
        # Find the message and verify it's an agent message belonging to the user
        message = (
            db.query(Message)
            .join(AgentInstance, Message.agent_instance_id == AgentInstance.id)
            .filter(
                Message.id == message_id,
                Message.sender_type == SenderType.AGENT,
                AgentInstance.user_id == user_id,
            )
            .first()
        )

        if not message:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND,
                detail="Agent message not found or access denied",
            )

        # Check if it already requires user input
        if message.requires_user_input:
            raise HTTPException(
                status_code=status.HTTP_400_BAD_REQUEST,
                detail="Message already requires user input",
            )

        # Update the field
        message.requires_user_input = True

        queued_messages = get_queued_user_messages(
            db, message.agent_instance_id, message_id
        )

        if not queued_messages:
            agent_instance = (
                db.query(AgentInstance)
                .filter(AgentInstance.id == message.agent_instance_id)
                .first()
            )
            if agent_instance:
                agent_instance.status = AgentStatus.AWAITING_INPUT

            await send_message_notifications(
                db=db,
                instance_id=message.agent_instance_id,
                content=message.content,
                requires_user_input=True,
            )

        db.commit()

        message_responses = [
            MessageResponse(
                id=str(msg.id),
                content=msg.content,
                sender_type=msg.sender_type.value,
                created_at=msg.created_at.isoformat(),
                requires_user_input=msg.requires_user_input,
            )
            for msg in (queued_messages or [])
        ]

        return {
            "success": True,
            "message_id": str(message_id),
            "agent_instance_id": str(message.agent_instance_id),
            "messages": message_responses,
            "status": "ok" if queued_messages is not None else "stale",
        }
    except HTTPException:
        db.rollback()
        raise
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
