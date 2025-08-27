"""
Simple webhook formatting for different integration types.
"""

from typing import Dict, Any, Optional


def format_webhook_payload(
    webhook_type: str,
    base_payload: Dict[str, Any],
    webhook_config: Optional[Dict[str, Any]] = None,
) -> tuple[Optional[str], Dict[str, str], Dict[str, Any]]:
    """
    Format webhook payload based on integration type.

    Args:
        webhook_type: Type of webhook ("default", "github", etc.)
        base_payload: Standard Omnara webhook payload
        webhook_config: Additional configuration for the webhook type

    Returns:
        Tuple of (url, headers, formatted_payload)
    """
    webhook_config = webhook_config or {}

    if webhook_type == "github":
        # GitHub repository_dispatch format
        repository = webhook_config.get("repository")
        if not repository:
            raise ValueError("GitHub webhook requires 'repository' in config")

        url = f"https://api.github.com/repos/{repository}/dispatches"
        headers = {
            "Accept": "application/vnd.github.v3+json",
            "X-GitHub-Api-Version": "2022-11-28",
        }
        payload = {
            "event_type": webhook_config.get("event_type", "omnara-trigger"),
            "client_payload": base_payload,
        }

    else:
        # Default format - just return as-is
        url = None  # Will use webhook_url from database
        headers = {}
        payload = base_payload

    return url, headers, payload
