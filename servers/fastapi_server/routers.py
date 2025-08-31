"""API routes for agent operations."""

import logging
from typing import Annotated
from uuid import UUID

from fastapi import (
    APIRouter,
    BackgroundTasks,
    Depends,
    File,
    HTTPException,
    UploadFile,
    status,
)
from shared.config.settings import settings
from shared.database import AgentInstance, AgentStatus, Message, SenderType, User
from shared.database.session import SessionLocal, get_db
from shared.storage import (
    enhance_messages_with_signed_urls,
    generate_attachment_path,
    get_storage_client,
    prepare_attachment_metadata,
    upload_image,
    validate_image_file,
)
from sqlalchemy.orm import Session
from sqlalchemy.orm.attributes import flag_modified
from servers.shared.db import (
    send_agent_message,
    end_session,
    get_or_create_agent_instance,
    get_queued_user_messages,
    create_user_message,
    update_session_title_if_needed,
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
    VerifyAuthResponse,
)

agent_router = APIRouter(tags=["agents"])
logger = logging.getLogger(__name__)


@agent_router.get("/auth/verify", response_model=VerifyAuthResponse)
def verify_auth_endpoint(
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> VerifyAuthResponse:
    """Verify API key authentication.

    This endpoint is used by n8n and other integrations to test credentials.
    Returns basic information about the authenticated user and API key.
    """
    try:
        # Get user information
        user = db.query(User).filter(User.id == user_id).first()
        if not user:
            raise HTTPException(
                status_code=status.HTTP_404_NOT_FOUND,
                detail="User not found",
            )

        # Get API key information (optional - just to show which key is being used)
        # Note: We can't identify the specific key from the JWT, but we know it's valid
        # if we got this far

        return VerifyAuthResponse(
            success=True,
            user_id=str(user.id),
            email=user.email,
            display_name=user.display_name,
            message="Authentication successful",
        )
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error in verify_auth_endpoint: {str(e)}")
        raise HTTPException(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            detail="Internal server error",
        )


@agent_router.post("/messages/agent", response_model=CreateMessageResponse)
async def create_agent_message_endpoint(
    request: CreateMessageRequest,
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> CreateMessageResponse:
    """Create a new agent message.

    This endpoint:
    - Creates or retrieves an agent instance
    - Creates a new message
    - Returns the message ID and any queued user messages
    - Sends notifications if requested
    """

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
            message_metadata=request.message_metadata,
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
                message_metadata=msg.message_metadata,
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


@agent_router.post("/messages/user", response_model=CreateUserMessageResponse)
def create_user_message_endpoint(
    request: CreateUserMessageRequest,
    background_tasks: BackgroundTasks,
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> CreateUserMessageResponse:
    """Create a user message.

    This endpoint:
    - Creates a user message for an existing agent instance
    - Optionally marks it as read (updates last_read_message_id)
    - Returns the message ID
    - Triggers any waiting webhooks (e.g., n8n workflows)
    - Generates session title if needed (in background)
    """

    try:
        result = create_user_message(
            db=db,
            agent_instance_id=request.agent_instance_id,
            content=request.content,
            user_id=user_id,
            mark_as_read=request.mark_as_read,
        )

        db.commit()

        # Add background task to update session title if needed
        def update_title_with_session():
            db_session = SessionLocal()
            try:
                update_session_title_if_needed(
                    db=db_session,
                    instance_id=UUID(result["instance_id"]),
                    user_message=request.content,
                )
            finally:
                db_session.close()

        background_tasks.add_task(update_title_with_session)

        return CreateUserMessageResponse(
            success=True,
            message_id=result["id"],
            marked_as_read=result["marked_as_read"],
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


@agent_router.get("/messages/pending", response_model=GetMessagesResponse)
def get_pending_messages(
    agent_instance_id: str,
    last_read_message_id: str | None,
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> GetMessagesResponse:
    """Get pending user messages for an agent instance.

    This endpoint:
    - Returns all user messages since the provided last_read_message_id
    - Updates the last_read_message_id to the latest message
    - Returns None status if another process has already read the messages
    """

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
                message_metadata=msg.message_metadata,
            )
            for msg in messages
        ]

        # Add signed URLs to any attachments in the messages
        if message_responses:
            storage_client = get_storage_client()
            message_responses = enhance_messages_with_signed_urls(
                message_responses, storage_client
            )

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


@agent_router.patch("/messages/{message_id}/request-input")
async def request_user_input_endpoint(
    message_id: UUID,
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> dict:
    """Update an agent message to request user input.

    This endpoint:
    - Updates the requires_user_input field from false to true
    - Only works on agent messages that don't already require input
    - Returns any queued user messages since this message
    - Triggers a notification via the database trigger
    """

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
                message_metadata=msg.message_metadata,
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


@agent_router.post("/sessions/end", response_model=EndSessionResponse)
def end_session_endpoint(
    request: EndSessionRequest,
    user_id: Annotated[str, Depends(get_current_user_id)],
    db: Session = Depends(get_db),
) -> EndSessionResponse:
    """End an agent session and mark it as completed.

    This endpoint:
    - Marks the agent instance as COMPLETED
    - Sets the session end time
    """

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


@agent_router.post("/messages/{message_id}/attachments")
async def upload_message_attachment(
    message_id: UUID,
    user_id: Annotated[str, Depends(get_current_user_id)],
    file: UploadFile = File(...),
    db: Session = Depends(get_db),
):
    """Upload an image attachment to a message.

    This endpoint:
    - Validates the image file (size, type)
    - Uploads to Supabase Storage
    - Updates the message's metadata with attachment info
    - Returns the attachment metadata with signed URL
    """
    try:
        # Get the message and verify ownership
        message = db.query(Message).filter(Message.id == message_id).first()
        if not message:
            raise HTTPException(status_code=404, detail="Message not found")

        # Verify the message belongs to the user's agent instance
        instance = (
            db.query(AgentInstance)
            .filter(AgentInstance.id == message.agent_instance_id)
            .first()
        )
        if not instance or str(instance.user_id) != user_id:
            raise HTTPException(status_code=403, detail="Access denied")

        # Read file data
        file_data = await file.read()
        mime_type = file.content_type or "application/octet-stream"

        # Validate the file
        is_valid, error_msg = validate_image_file(file_data, mime_type)
        if not is_valid:
            raise HTTPException(status_code=400, detail=error_msg)

        # Generate storage path
        storage_path = generate_attachment_path(
            user_id=user_id,
            instance_id=str(instance.id),
            message_id=str(message.id),
            filename=file.filename or "image",
        )

        # Upload to Supabase
        storage_client = get_storage_client()
        upload_result = upload_image(
            client=storage_client,
            bucket=settings.storage_bucket,
            path=storage_path,
            file_data=file_data,
            mime_type=mime_type,
        )

        # Prepare attachment metadata
        attachment = prepare_attachment_metadata(
            bucket=upload_result["bucket"],
            path=upload_result["path"],
            filename=file.filename or "image",
            size=len(file_data),
            mime_type=mime_type,
        )

        # Update message metadata
        if message.message_metadata is None:
            message.message_metadata = {"attachments": []}
        elif "attachments" not in message.message_metadata:
            message.message_metadata["attachments"] = []

        message.message_metadata["attachments"].append(attachment)

        # Mark the message_metadata as modified for SQLAlchemy to detect the change
        flag_modified(message, "message_metadata")

        db.commit()

        # Add signed URL to the attachment before returning
        attachment["signed_url"] = storage_client.storage.from_(
            attachment["bucket"]
        ).create_signed_url(attachment["path"], settings.storage_signed_url_expiry)[
            "signedURL"
        ]

        return {
            "success": True,
            "attachment": attachment,
        }

    except HTTPException:
        db.rollback()
        raise
    except Exception as e:
        db.rollback()
        logger.error(f"Failed to upload attachment: {str(e)}")
        raise HTTPException(
            status_code=500,
            detail=f"Failed to upload attachment: {str(e)}",
        )
