"""Utility functions for the Omnara SDK."""

import uuid
from typing import Optional, Union, Dict, Any


def validate_agent_instance_id(
    agent_instance_id: Optional[Union[str, uuid.UUID]],
) -> str:
    """Validate and convert agent_instance_id to string.

    Args:
        agent_instance_id: UUID string, UUID object, or None

    Returns:
        Validated UUID string

    Raises:
        ValueError: If agent_instance_id is not a valid UUID
    """
    if agent_instance_id is None:
        raise ValueError("agent_instance_id cannot be None")

    if isinstance(agent_instance_id, str):
        try:
            # Validate it's a valid UUID
            uuid.UUID(agent_instance_id)
            return agent_instance_id
        except ValueError:
            raise ValueError("agent_instance_id must be a valid UUID string")
    elif isinstance(agent_instance_id, uuid.UUID):
        return str(agent_instance_id)
    else:
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
) -> Dict[str, Any]:
    """Build request data for creating a message.

    Args:
        content: Message content
        agent_instance_id: Agent instance ID (already validated)
        requires_user_input: Whether message requires user input
        agent_type: Optional agent type
        send_push: Optional push notification flag
        send_email: Optional email notification flag
        send_sms: Optional SMS notification flag
        git_diff: Optional git diff content

    Returns:
        Dictionary of request data
    """
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
        data["git_diff"] = git_diff

    return data
