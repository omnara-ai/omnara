"""
Backend API routes for prompt queue management.

This module provides endpoints for managing queued prompts for agent instances.
"""

from uuid import UUID

from fastapi import APIRouter, Depends, HTTPException
from shared.database.models import User
from shared.database.session import get_db
from shared.database.enums import PromptQueueStatus
from sqlalchemy.orm import Session

from ..auth.dependencies import get_current_user
from ..models import (
    PromptQueueCreate,
    PromptQueueItemResponse,
    PromptQueueReorder,
    PromptQueueUpdate,
    PromptQueueStatusResponse,
    ClearQueueResponse,
)
from servers.shared.db.queries import (
    add_prompts_to_queue,
    get_queue_for_instance,
    delete_queue_item,
    update_queue_item,
    reorder_queue,
    clear_queue,
    get_queue_status,
)

router = APIRouter(tags=["prompt-queue"])


@router.post(
    "/agent-instances/{instance_id}/prompt-queue",
    response_model=list[PromptQueueItemResponse],
)
def add_to_queue(
    instance_id: str,
    request: PromptQueueCreate,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Add prompts to the queue for an agent instance.

    Args:
        instance_id: The agent instance ID
        request: List of prompts to add
        current_user: Authenticated user
        db: Database session

    Returns:
        List of created queue items

    Raises:
        HTTPException: If instance not found or user doesn't have access
    """
    try:
        queue_items = add_prompts_to_queue(
            db=db,
            agent_instance_id=UUID(instance_id),
            user_id=current_user.id,
            prompts=request.prompts,
        )
        db.commit()
        return queue_items
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(status_code=500, detail=f"Failed to add prompts: {str(e)}")


@router.get(
    "/agent-instances/{instance_id}/prompt-queue",
    response_model=list[PromptQueueItemResponse],
)
def get_queue(
    instance_id: str,
    status: PromptQueueStatus | None = None,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Get all queued prompts for an agent instance.

    Args:
        instance_id: The agent instance ID
        status: Optional status filter (pending, sent, failed, cancelled)
        current_user: Authenticated user
        db: Database session

    Returns:
        List of queue items ordered by position

    Raises:
        HTTPException: If instance not found or user doesn't have access
    """
    try:
        queue_items = get_queue_for_instance(
            db=db,
            agent_instance_id=UUID(instance_id),
            user_id=current_user.id,
            status_filter=status,
        )
        return queue_items
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        raise HTTPException(
            status_code=500, detail=f"Failed to get queue: {str(e)}"
        )


@router.put(
    "/agent-instances/{instance_id}/prompt-queue/reorder",
    response_model=list[PromptQueueItemResponse],
)
def reorder_queue_items(
    instance_id: str,
    request: PromptQueueReorder,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Reorder queue items for an agent instance.

    Args:
        instance_id: The agent instance ID
        request: List of queue item IDs in desired order
        current_user: Authenticated user
        db: Database session

    Returns:
        List of reordered queue items

    Raises:
        HTTPException: If instance not found, user doesn't have access, or IDs don't match
    """
    try:
        queue_item_ids = [UUID(id_str) for id_str in request.queue_item_ids]
        queue_items = reorder_queue(
            db=db,
            agent_instance_id=UUID(instance_id),
            user_id=current_user.id,
            queue_item_ids=queue_item_ids,
        )
        db.commit()
        return queue_items
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=500, detail=f"Failed to reorder queue: {str(e)}"
        )


@router.delete("/prompt-queue/{queue_item_id}", status_code=204)
def delete_queue_item_endpoint(
    queue_item_id: str,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Delete a queue item.

    Args:
        queue_item_id: The queue item ID to delete
        current_user: Authenticated user
        db: Database session

    Raises:
        HTTPException: If queue item not found or user doesn't have access
    """
    try:
        delete_queue_item(
            db=db,
            queue_item_id=UUID(queue_item_id),
            user_id=current_user.id,
        )
        db.commit()
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=500, detail=f"Failed to delete queue item: {str(e)}"
        )


@router.patch(
    "/prompt-queue/{queue_item_id}",
    response_model=PromptQueueItemResponse,
)
def update_queue_item_endpoint(
    queue_item_id: str,
    request: PromptQueueUpdate,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Update a queue item's prompt text.

    Args:
        queue_item_id: The queue item ID to update
        request: Updated prompt text
        current_user: Authenticated user
        db: Database session

    Returns:
        The updated queue item

    Raises:
        HTTPException: If queue item not found, user doesn't have access, or item is not pending
    """
    try:
        queue_item = update_queue_item(
            db=db,
            queue_item_id=UUID(queue_item_id),
            user_id=current_user.id,
            new_prompt_text=request.prompt_text,
        )
        db.commit()
        return queue_item
    except ValueError as e:
        raise HTTPException(status_code=400, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=500, detail=f"Failed to update queue item: {str(e)}"
        )


@router.post(
    "/agent-instances/{instance_id}/prompt-queue/clear",
    response_model=ClearQueueResponse,
)
def clear_queue_endpoint(
    instance_id: str,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Clear all pending prompts from the queue.

    Args:
        instance_id: The agent instance ID
        current_user: Authenticated user
        db: Database session

    Returns:
        Number of items deleted

    Raises:
        HTTPException: If instance not found or user doesn't have access
    """
    try:
        deleted_count = clear_queue(
            db=db,
            agent_instance_id=UUID(instance_id),
            user_id=current_user.id,
        )
        db.commit()
        return ClearQueueResponse(deleted_count=deleted_count)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        db.rollback()
        raise HTTPException(
            status_code=500, detail=f"Failed to clear queue: {str(e)}"
        )


@router.get(
    "/agent-instances/{instance_id}/prompt-queue/status",
    response_model=PromptQueueStatusResponse,
)
def get_queue_status_endpoint(
    instance_id: str,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Get queue statistics for an agent instance.

    Args:
        instance_id: The agent instance ID
        current_user: Authenticated user
        db: Database session

    Returns:
        Queue statistics (total, pending, sent, failed, next_position)

    Raises:
        HTTPException: If instance not found or user doesn't have access
    """
    try:
        status = get_queue_status(
            db=db,
            agent_instance_id=UUID(instance_id),
            user_id=current_user.id,
        )
        return PromptQueueStatusResponse(**status)
    except ValueError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        raise HTTPException(
            status_code=500, detail=f"Failed to get queue status: {str(e)}"
        )
