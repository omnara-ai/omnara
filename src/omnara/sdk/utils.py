"""Utility functions for the Omnara SDK."""

from __future__ import annotations

import base64
import uuid
from typing import Any, Dict, Optional, Union

from shared.message_types import MessageMetadataError, normalize_message_metadata

from .models import Message


def validate_agent_instance_id(
    agent_instance_id: Optional[Union[str, uuid.UUID]],
) -> str:
    """Validate and convert agent_instance_id to string."""

    if agent_instance_id is None:
        raise ValueError("agent_instance_id cannot be None")

    if isinstance(agent_instance_id, str):
        try:
            uuid.UUID(agent_instance_id)
            return agent_instance_id
        except ValueError as exc:  # pragma: no cover - defensive
            raise ValueError("agent_instance_id must be a valid UUID string") from exc

    if isinstance(agent_instance_id, uuid.UUID):
        return str(agent_instance_id)

    raise ValueError("agent_instance_id must be a string or UUID object")


def build_message_request_data(
    content: str,
    agent_instance_id: str,
    requires_user_input: bool,
    agent_type: Optional[str] = None,
    send_push: Optional[bool] = None,
    send_email: Optional[bool] = None,
    send_sms: Optional[bool] = None,
    git_diff: Optional[str] = None,
    message_metadata: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """Build request payload for creating a message."""

    data: Dict[str, Any] = {
        "content": content,
        "requires_user_input": requires_user_input,
        "agent_instance_id": agent_instance_id,
    }
    if agent_type:
        data["agent_type"] = agent_type
    if send_push is not None:
        data["send_push"] = send_push
    if send_email is not None:
        data["send_email"] = send_email
    if send_sms is not None:
        data["send_sms"] = send_sms
    if git_diff is not None:
        try:
            encoded = base64.b64encode(git_diff.encode("utf-8")).decode("ascii")
            data["git_diff"] = encoded
        except Exception:  # pragma: no cover - fallback for unexpected encoding
            data["git_diff"] = git_diff
    if message_metadata is not None:
        data["message_metadata"] = message_metadata

    return data


def normalize_metadata_for_request(
    metadata: Optional[Dict[str, Any]],
) -> Optional[Dict[str, Any]]:
    """Normalize metadata before sending it to the API."""

    if metadata is None:
        return None

    try:
        return normalize_message_metadata(metadata) or None
    except MessageMetadataError as exc:
        raise ValueError(f"Invalid message metadata: {exc}") from exc


def parse_api_message(data: Dict[str, Any]) -> Message:
    """Create a Message dataclass from API response data."""

    return Message(
        id=data.get("id", ""),
        content=data.get("content", ""),
        sender_type=data.get("sender_type", ""),
        created_at=data.get("created_at", ""),
        requires_user_input=data.get("requires_user_input", False),
        sender_user_id=data.get("sender_user_id"),
        sender_user_email=data.get("sender_user_email"),
        sender_user_display_name=data.get("sender_user_display_name"),
        message_type=data.get("message_type"),
        message_metadata=data.get("message_metadata"),
    )
