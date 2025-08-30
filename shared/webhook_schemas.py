"""
Webhook type schemas for frontend form generation and backend validation.
"""

from typing import Dict, Any, List, Optional
from pydantic import BaseModel, Field
from enum import Enum
import re
import json


class FieldType(str, Enum):
    """Field types for webhook configuration."""

    STRING = "string"  # Single line text input
    TEXT = "text"  # Multi-line text area
    PASSWORD = "password"  # Should be masked in UI
    SELECT = "select"
    BOOLEAN = "boolean"
    NUMBER = "number"
    URL = "url"


class WebhookField(BaseModel):
    """Schema for a single webhook configuration field."""

    name: str = Field(..., description="Field identifier")
    label: str = Field(..., description="Display label for UI")
    type: FieldType = Field(..., description="Field type")
    required: bool = Field(default=True, description="Is this field required?")
    description: Optional[str] = Field(
        default=None, description="Help text for the field"
    )
    placeholder: Optional[str] = Field(default=None, description="Placeholder text")
    default: Optional[Any] = Field(default=None, description="Default value")
    options: Optional[List[Dict[str, str]]] = Field(
        default=None, description="Options for select fields"
    )
    validation_regex: Optional[str] = Field(
        default=None, description="Regex pattern for validation"
    )
    is_secret: bool = Field(
        default=False,
        description="Whether this field contains sensitive data (for UI masking)",
    )

    class Config:
        json_schema_extra = {
            "example": {
                "name": "repository",
                "label": "Repository",
                "type": "string",
                "required": True,
                "description": "GitHub repository in format owner/repo",
                "placeholder": "octocat/hello-world",
                "validation_regex": "^[a-zA-Z0-9_-]+/[a-zA-Z0-9_-]+$",
            }
        }


class WebhookTypeSchema(BaseModel):
    """Complete schema for a webhook type."""

    id: str = Field(..., description="Unique identifier for the webhook type")
    name: str = Field(..., description="Display name for the webhook type")
    description: str = Field(..., description="Description of what this webhook does")
    icon: Optional[str] = Field(default=None, description="Icon identifier for UI")

    # Build-time fields - configured once and stored in webhook_config
    build_fields: List[WebhookField] = Field(
        ..., description="Configuration fields set when creating/editing the webhook"
    )

    # Runtime fields - provided with each agent instance creation request
    # These are automatically available as {user.field_name} in templates
    runtime_fields: List[WebhookField] = Field(
        default_factory=list,
        description="Fields provided at runtime when creating agent instances",
    )

    # Request construction templates
    # Available template variables:
    # - {field_name} for build-time config fields
    # - {user.field_name} for runtime fields
    # - {backend.field_name} for backend-generated fields
    url_template: Optional[str] = Field(
        default=None,
        description="URL template with substitutions",
    )
    headers_template: Dict[str, str] = Field(
        default_factory=dict,
        description="Headers template with substitutions",
    )
    payload_template: Optional[Dict[str, Any]] = Field(
        default=None,
        description="Payload structure template. None = default payload with all runtime and backend fields",
    )

    class Config:
        json_schema_extra = {
            "example": {
                "id": "github",
                "name": "GitHub Actions",
                "description": "Trigger GitHub Actions via repository_dispatch",
                "icon": "github",
                "build_fields": [...],
                "runtime_fields": [...],
                "url_template": "https://api.github.com/repos/{repository}/dispatches",
            }
        }


# Define the webhook type schemas
WEBHOOK_TYPES: Dict[str, WebhookTypeSchema] = {
    "DEFAULT": WebhookTypeSchema(
        id="DEFAULT",
        name="Custom Webhook",
        description="Send requests to any custom webhook endpoint",
        icon="webhook",
        build_fields=[
            WebhookField(
                name="url",
                label="Webhook URL",
                type=FieldType.URL,
                required=True,
                description="The URL to send webhook requests to",
                placeholder="https://your-server.com/webhook",
            ),
            WebhookField(
                name="api_key",
                label="API Key",
                type=FieldType.PASSWORD,
                required=False,
                description="Bearer token for authentication",
                placeholder="your-secret-key",
                is_secret=True,
            ),
            WebhookField(
                name="custom_headers",
                label="Custom Headers",
                type=FieldType.STRING,
                required=False,
                description="Additional headers as JSON object",
                placeholder='{"X-Custom-Header": "value"}',
                validation_regex=r"^\s*\{.*\}\s*$",  # Basic JSON object validation
            ),
        ],
        runtime_fields=[
            WebhookField(
                name="prompt",
                label="Prompt",
                type=FieldType.TEXT,
                required=True,
                description="The task or question for the agent",
                placeholder="Describe what you want the agent to do...",
            ),
        ],
        url_template="{build.url}",  # URL from config field
        headers_template={
            "Content-Type": "application/json",
            "Authorization": "Bearer {build.api_key}",  # Will be empty if no api_key
        },
        payload_template={
            "agent_instance_id": "{backend.agent_instance_id}",
            "agent_type": "{backend.agent_type}",
            "omnara_api_key": "{backend.omnara_api_key}",
            "prompt": "{runtime.prompt}",
        },
    ),
    "GITHUB": WebhookTypeSchema(
        id="GITHUB",
        name="GitHub Actions",
        description="Trigger GitHub Actions workflows via repository_dispatch",
        icon="github",
        build_fields=[
            WebhookField(
                name="repository",
                label="Repository",
                type=FieldType.STRING,
                required=True,
                description="GitHub repository (format: owner/repo)",
                placeholder="octocat/hello-world",
                validation_regex=r"^[a-zA-Z0-9][a-zA-Z0-9-_]*/[a-zA-Z0-9][a-zA-Z0-9-_\.]*$",
            ),
            WebhookField(
                name="github_token",
                label="GitHub Personal Access Token",
                type=FieldType.PASSWORD,
                required=True,
                description="PAT with 'repo' scope for triggering workflows",
                placeholder="ghp_xxxxxxxxxxxx",
                is_secret=True,
            ),
            WebhookField(
                name="event_type",
                label="Event Type",
                type=FieldType.STRING,
                required=False,
                default="omnara-trigger",
                description="The event_type for repository_dispatch",
                placeholder="omnara-trigger",
            ),
        ],
        runtime_fields=[
            WebhookField(
                name="prompt",
                label="Prompt",
                type=FieldType.TEXT,
                required=True,
                description="The task or question for the agent",
                placeholder="Describe what you want the agent to do...",
            ),
            WebhookField(
                name="name",
                label="Branch Name",
                type=FieldType.STRING,
                required=False,
                description="Git branch name for the agent to work on",
                placeholder="main",
            ),
        ],
        url_template="https://api.github.com/repos/{build.repository}/dispatches",
        headers_template={
            "Content-Type": "application/json",
            "Accept": "application/vnd.github.v3+json",
            "X-GitHub-Api-Version": "2022-11-28",
            "Authorization": "Bearer {build.github_token}",
        },
        payload_template={
            "event_type": "{build.event_type}",
            "client_payload": {
                "agent_instance_id": "{backend.agent_instance_id}",
                "prompt": "{runtime.prompt}",
                "agent_type": "{backend.agent_type}",
                "omnara_api_key": "{backend.omnara_api_key}",
                "name": "{runtime.name}",
                "worktree_name": "{runtime.worktree_name}",
            },
        },
    ),
    "OMNARA_SERVE": WebhookTypeSchema(
        id="OMNARA_SERVE",
        name="Omnara Serve",
        description="Trigger an Omnara serve endpoint with git worktree and branch support",
        icon="omnara",
        build_fields=[
            WebhookField(
                name="url",
                label="Omnara Serve URL",
                type=FieldType.URL,
                required=True,
                description="The Omnara serve endpoint URL",
                placeholder="https://your-server.com/omnara",
            ),
            WebhookField(
                name="api_key",
                label="API Key",
                type=FieldType.PASSWORD,
                required=False,
                description="Bearer token for authentication",
                placeholder="your-secret-key",
                is_secret=True,
            ),
        ],
        runtime_fields=[
            WebhookField(
                name="prompt",
                label="Prompt",
                type=FieldType.TEXT,
                required=True,
                description="The task or question for the agent",
                placeholder="Describe what you want the agent to do...",
            ),
            WebhookField(
                name="worktree_name",
                label="Worktree Name",
                type=FieldType.STRING,
                required=False,
                description="Git worktree name for isolated development",
                placeholder="feature-xyz",
            ),
            WebhookField(
                name="branch_name",
                label="Branch Name",
                type=FieldType.STRING,
                required=False,
                description="Git branch name to use in the worktree",
                placeholder="main",
            ),
        ],
        url_template="{build.url}",  # URL from config field
        headers_template={
            "Content-Type": "application/json",
            "Authorization": "Bearer {build.api_key}",
            "X-Omnara-Api-Key": "{backend.omnara_api_key}",
        },
        payload_template={
            "agent_instance_id": "{backend.agent_instance_id}",
            "agent_type": "{backend.agent_type}",
            "prompt": "{runtime.prompt}",
            "worktree_name": "{runtime.worktree_name}",
            "branch_name": "{runtime.branch_name}",
        },
    ),
}


def get_webhook_types() -> List[Dict[str, Any]]:
    """
    Get all available webhook types for frontend consumption.

    Returns a list of webhook type schemas that can be used
    to generate dynamic forms in the frontend.
    """
    return [schema.model_dump() for schema in WEBHOOK_TYPES.values()]


def get_webhook_type_schema(webhook_type_id: str) -> Optional[WebhookTypeSchema]:
    """Get the schema for a specific webhook type."""
    return WEBHOOK_TYPES.get(webhook_type_id)


def get_runtime_field_names(webhook_type_id: str) -> set[str]:
    """
    Get the names of all runtime fields for a webhook type.

    Args:
        webhook_type_id: The webhook type identifier

    Returns:
        Set of all runtime field names (both required and optional)
    """
    schema = get_webhook_type_schema(webhook_type_id)
    if not schema:
        return set()

    return {field.name for field in schema.runtime_fields}


def validate_webhook_config(
    webhook_type_id: str, config: Dict[str, Any]
) -> tuple[bool, Optional[str]]:
    """
    Validate webhook configuration (build fields) against its schema.

    Returns:
        Tuple of (is_valid, error_message)
    """
    schema = get_webhook_type_schema(webhook_type_id)
    if not schema:
        return False, f"Unknown webhook type: {webhook_type_id}"

    # Check required build fields
    for field in schema.build_fields:
        if field.required and field.name not in config:
            return False, f"Missing required field: {field.label}"

        # Validate regex if provided
        if field.validation_regex and field.name in config:
            if not re.match(field.validation_regex, str(config[field.name])):
                return False, f"Invalid format for {field.label}"

    return True, None


def validate_runtime_fields(
    webhook_type_id: str, runtime_data: Dict[str, Any]
) -> tuple[bool, Optional[str]]:
    """
    Validate runtime fields against webhook schema.

    Returns:
        tuple: (is_valid, error_message)
    """
    schema = get_webhook_type_schema(webhook_type_id)
    if not schema:
        return False, f"Unknown webhook type: {webhook_type_id}"

    # Check required runtime fields
    for field in schema.runtime_fields:
        if field.required:
            value = runtime_data.get(field.name)
            if value is None or (isinstance(value, str) and not value.strip()):
                return False, f"Missing required field: {field.label}"

        # Validate regex if provided and field has a value
        if field.validation_regex and field.name in runtime_data:
            value = runtime_data.get(field.name)
            if value and not re.match(field.validation_regex, str(value)):
                return False, f"Invalid format for {field.label}"

    return True, None


def process_template(
    template: Any,
    webhook_config: Dict[str, Any],
    user_request: Dict[str, Any],
    backend_fields: Dict[str, Any],
) -> Any:
    """
    Recursively process a template, replacing placeholders with actual values.

    Placeholders:
    - {build.field_name} - replaced with field value from webhook_config (build-time configuration)
    - {runtime.field_name} - replaced with field value from user_request (runtime input fields)
    - {backend.field_name} - replaced with field value from backend_fields (agent_instance_id, etc.)

    Args:
        template: The template structure to process
        webhook_config: Build-time webhook configuration fields (repository, api_key, etc.)
        user_request: Runtime input fields (prompt, name, worktree_name, etc.)
        backend_fields: Backend-provided fields (agent_instance_id, omnara_api_key, agent_type)
    """
    if isinstance(template, str):
        result = template

        # Replace all backend field placeholders
        backend_pattern = re.compile(r"\{backend\.([^}]+)\}")
        for match in backend_pattern.finditer(template):
            field_name = match.group(1)
            if field_name in backend_fields:
                value = backend_fields[field_name]
                if value is not None:
                    result = result.replace(match.group(0), str(value))

        # Replace all runtime field placeholders
        runtime_pattern = re.compile(r"\{runtime\.([^}]+)\}")
        for match in runtime_pattern.finditer(result):
            field_name = match.group(1)
            if field_name in user_request:
                value = user_request[field_name]
                if value is not None:
                    result = result.replace(match.group(0), str(value))

        # Replace all build field placeholders
        build_pattern = re.compile(r"\{build\.([^}]+)\}")
        for match in build_pattern.finditer(result):
            field_name = match.group(1)
            if field_name in webhook_config:
                value = webhook_config[field_name]
                if value is not None:
                    result = result.replace(match.group(0), str(value))

        return result
    elif isinstance(template, dict):
        # Recursively process dict
        result = {}
        for k, v in template.items():
            processed = process_template(
                v, webhook_config, user_request, backend_fields
            )
            # Skip fields that couldn't be substituted (still contain template placeholders)
            if not (
                isinstance(processed, str)
                and (
                    "{backend." in processed
                    or "{runtime." in processed
                    or "{build." in processed
                )
            ):
                result[k] = processed
        return result
    elif isinstance(template, list):
        return [
            process_template(item, webhook_config, user_request, backend_fields)
            for item in template
        ]
    else:
        return template


def format_webhook_request(
    webhook_type_id: str,
    webhook_config: Dict[str, Any],
    user_request: Dict[str, Any],
    backend_fields: Dict[str, Any],
) -> tuple[str, Dict[str, str], Dict[str, Any]]:
    """
    Format a webhook request based on the webhook type schema.

    Args:
        webhook_type_id: The webhook type identifier
        webhook_config: The user's webhook configuration (repository, api_key, etc.)
        user_request: User's request data (prompt, name, worktree_name)
        backend_fields: Backend-generated fields (agent_instance_id, agent_type, omnara_api_key)

    Returns:
        Tuple of (url, headers, formatted_payload)
    """
    schema = get_webhook_type_schema(webhook_type_id)
    if not schema:
        raise ValueError(f"Unknown webhook type: {webhook_type_id}")

    # Apply defaults to webhook config
    config_with_defaults = webhook_config.copy()
    for field in schema.build_fields:
        if field.name not in config_with_defaults and field.default is not None:
            config_with_defaults[field.name] = field.default

    # Parse custom_headers JSON for DEFAULT type (preprocessing step)
    if webhook_type_id == "DEFAULT" and "custom_headers" in config_with_defaults:
        try:
            custom_headers_str = config_with_defaults["custom_headers"]
            if custom_headers_str:
                custom_headers = json.loads(custom_headers_str)
                if isinstance(custom_headers, dict):
                    # Store parsed headers in config for template processing
                    config_with_defaults["_parsed_custom_headers"] = custom_headers
        except (json.JSONDecodeError, TypeError):
            pass

    # Process URL template
    url = process_template(
        schema.url_template or "{build.url}",  # Default to {build.url} if no template
        config_with_defaults,
        user_request,
        backend_fields,
    )

    # Process header templates
    headers = {}
    for header_key, header_template in schema.headers_template.items():
        header_value = process_template(
            header_template, config_with_defaults, user_request, backend_fields
        )
        # Only include non-empty headers
        if header_value and not (
            isinstance(header_value, str)
            and header_value.startswith("Bearer ")
            and len(header_value) == 7
        ):
            headers[header_key] = header_value

    # Add parsed custom headers if available
    if "_parsed_custom_headers" in config_with_defaults:
        headers.update(config_with_defaults["_parsed_custom_headers"])

    # Process payload template
    if schema.payload_template is not None:
        payload = process_template(
            schema.payload_template, config_with_defaults, user_request, backend_fields
        )
    else:
        # No template - shouldn't happen with current schemas
        payload = {}

    return url, headers, payload
