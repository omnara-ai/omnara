"""
API endpoints for webhook type schemas.
"""

from fastapi import APIRouter, HTTPException
from typing import List, Dict, Any

from shared.webhook_schemas import (
    get_webhook_types,
    get_webhook_type_schema,
    validate_webhook_config,
)

router = APIRouter(prefix="/api/v1/webhook-types", tags=["webhook-types"])


@router.get("", response_model=List[Dict[str, Any]])
async def list_webhook_types() -> List[Dict[str, Any]]:
    """
    Get all available webhook type schemas.

    This endpoint returns the complete schema for each webhook type,
    including all fields and their validation rules. The frontend
    can use this to dynamically generate forms.
    """
    return get_webhook_types()


@router.get("/{webhook_type_id}", response_model=Dict[str, Any])
async def get_webhook_type(webhook_type_id: str) -> Dict[str, Any]:
    """
    Get the schema for a specific webhook type.

    Args:
        webhook_type_id: The webhook type identifier (e.g., "default", "github")

    Returns:
        The complete schema for the webhook type

    Raises:
        404: If the webhook type doesn't exist
    """
    schema = get_webhook_type_schema(webhook_type_id)
    if not schema:
        raise HTTPException(
            status_code=404, detail=f"Webhook type '{webhook_type_id}' not found"
        )

    return schema.model_dump()


@router.post("/{webhook_type_id}/validate")
async def validate_webhook_configuration(
    webhook_type_id: str, config: Dict[str, Any]
) -> Dict[str, Any]:
    """
    Validate a webhook configuration against its schema.

    This can be used by the frontend to validate form data
    before submitting to create an agent.

    Args:
        webhook_type_id: The webhook type identifier
        config: The configuration to validate

    Returns:
        Validation result with any error messages
    """
    is_valid, error_message = validate_webhook_config(webhook_type_id, config)

    return {"valid": is_valid, "error": error_message}
