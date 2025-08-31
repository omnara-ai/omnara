"""Storage utility functions for handling images and attachments."""

import uuid
from pathlib import Path
from typing import Any

from shared.config.settings import settings
from shared.supabase import get_supabase_client
from supabase import Client

# Alias for clarity in storage context
get_storage_client = get_supabase_client


def validate_image_file(
    file_data: bytes, mime_type: str, max_size: int | None = None
) -> tuple[bool, str | None]:
    """
    Validate an image file.

    Args:
        file_data: The file content as bytes
        mime_type: The MIME type of the file
        max_size: Maximum file size in bytes (uses settings default if not provided)

    Returns:
        Tuple of (is_valid, error_message)
    """
    if max_size is None:
        max_size = settings.storage_max_file_size

    # Check file size
    if len(file_data) > max_size:
        return False, f"File size exceeds maximum of {max_size / 1024 / 1024:.1f}MB"

    # Check MIME type
    allowed_types = settings.storage_allowed_mime_types
    if mime_type not in allowed_types:
        return (
            False,
            f"File type {mime_type} not allowed. Allowed types: {', '.join(allowed_types)}",
        )

    return True, None


def generate_attachment_path(
    user_id: str,
    instance_id: str,
    message_id: str,
    filename: str,
) -> str:
    """
    Generate a structured path for storing attachments.

    Args:
        user_id: The user's UUID
        instance_id: The agent instance UUID
        message_id: The message UUID
        filename: The original filename

    Returns:
        A structured path like: user_id/instance_id/message_id/uuid_filename
    """
    # Extract file extension
    file_ext = Path(filename).suffix
    # Generate unique filename to avoid collisions
    unique_filename = f"{uuid.uuid4()}{file_ext}"

    return f"{user_id}/{instance_id}/{message_id}/{unique_filename}"


def upload_image(
    client: Client,
    bucket: str,
    path: str,
    file_data: bytes,
    mime_type: str,
) -> dict[str, Any]:
    """
    Upload an image to Supabase storage.

    Args:
        client: Supabase client
        bucket: Storage bucket name
        path: Path within the bucket
        file_data: File content as bytes
        mime_type: MIME type of the file

    Returns:
        Dict with upload result including the path
    """
    # Supabase SDK accepts bytes directly
    response = client.storage.from_(bucket).upload(
        path=path,
        file=file_data,
        file_options={
            "content-type": mime_type,
            "cache-control": "3600",
            "upsert": "false",  # Don't overwrite existing files
        },
    )

    # Check if upload was successful
    # The response will have path if successful
    if not response or (hasattr(response, "path") and not response.path):
        raise Exception("Upload failed")

    return {
        "path": path,
        "bucket": bucket,
    }


def create_signed_url(
    client: Client,
    bucket: str,
    path: str,
    expires_in: int | None = None,
) -> str:
    """
    Create a signed URL for private image access.

    Args:
        client: Supabase client
        bucket: Storage bucket name
        path: Path to the file within the bucket
        expires_in: Expiration time in seconds (uses settings default if not provided)

    Returns:
        Signed URL string
    """
    if expires_in is None:
        expires_in = settings.storage_signed_url_expiry

    response = client.storage.from_(bucket).create_signed_url(path, expires_in)

    if "error" in response and response["error"]:
        raise Exception(f"Failed to create signed URL: {response['error']}")

    return response["signedURL"]


def enhance_messages_with_signed_urls(
    messages: list[Any], client: Client | None = None
) -> list[Any]:
    """
    Add signed URLs to message attachments.

    Args:
        messages: List of message objects (can be dicts or objects with message_metadata)
        client: Supabase client (creates new one if not provided)

    Returns:
        Messages with signed URLs added to attachments
    """
    if client is None:
        client = get_storage_client()

    for message in messages:
        # Handle both dict and object access patterns
        metadata = None
        if hasattr(message, "message_metadata"):
            metadata = message.message_metadata
        elif isinstance(message, dict) and "message_metadata" in message:
            metadata = message["message_metadata"]

        if metadata and "attachments" in metadata:
            for attachment in metadata["attachments"]:
                try:
                    attachment["signed_url"] = create_signed_url(
                        client,
                        attachment["bucket"],
                        attachment["path"],
                    )
                except Exception as e:
                    # Log error but don't fail the entire request
                    print(f"Failed to create signed URL for attachment: {e}")
                    attachment["signed_url"] = None

    return messages


def prepare_attachment_metadata(
    bucket: str,
    path: str,
    filename: str,
    size: int,
    mime_type: str,
    provider: str = "supabase",
) -> dict[str, Any]:
    """
    Prepare attachment metadata for storage in message_metadata.

    Args:
        bucket: Storage bucket name
        path: Path within the bucket
        filename: Original filename
        size: File size in bytes
        mime_type: MIME type of the file
        provider: Storage provider (default: "supabase")

    Returns:
        Attachment metadata dict
    """
    return {
        "id": str(uuid.uuid4()),
        "provider": provider,
        "type": "image",
        "bucket": bucket,
        "path": path,
        "filename": filename,
        "size": size,
        "mime_type": mime_type,
    }
