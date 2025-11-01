"""Database utility functions."""

import logging
import re
from datetime import datetime, timezone
from typing import Optional
from uuid import UUID

from sqlalchemy.orm import Session


def is_valid_git_diff(diff: Optional[str]) -> bool:
    """Validate if a string is a valid git diff.

    Checks for:
    - Basic git diff format markers
    - Proper structure
    - Not just random text

    Args:
        diff: The string to validate as a git diff

    Returns:
        True if valid git diff format, False otherwise
    """
    if not diff or not isinstance(diff, str):
        return False

    # Check for essential git diff patterns
    has_diff_header = re.search(r"^diff --git", diff, re.MULTILINE) is not None
    has_index_line = (
        re.search(r"^index [a-f0-9]+\.\.[a-f0-9]+", diff, re.MULTILINE) is not None
    )
    has_file_markers = (
        re.search(r"^--- ", diff, re.MULTILINE) is not None
        and re.search(r"^\+\+\+ ", diff, re.MULTILINE) is not None
    )
    has_hunk_header = re.search(r"^@@[ \-\+,0-9]+@@", diff, re.MULTILINE) is not None

    # For new files (untracked), we might not have index lines
    has_new_file = re.search(r"^new file mode", diff, re.MULTILINE) is not None

    # A valid diff should have:
    # 1. diff --git header
    # 2. Either (index line) OR (new file mode)
    # 3. File markers (--- and +++)
    # 4. At least one hunk header (@@)

    is_valid = (
        has_diff_header
        and (has_index_line or has_new_file)
        and has_file_markers
        and has_hunk_header
    )

    # Additional check: should have some actual diff content (lines starting with +, -, or space)
    has_diff_content = re.search(r"^[ \+\-]", diff, re.MULTILINE) is not None

    return is_valid and has_diff_content


def sanitize_git_diff(diff: Optional[str]) -> Optional[str]:
    """Sanitize and validate a git diff for storage.

    Args:
        diff: The git diff string to sanitize (None means no update needed)

    Returns:
        - Original diff string if valid
        - Empty string if diff is empty (clears the git diff)
        - None if diff is invalid or not provided
    """
    if diff is None:
        return None

    # Strip excessive whitespace
    diff = diff.strip()

    # If empty after stripping, return empty string (valid case)
    if not diff:
        return ""

    # Check if it's a valid git diff
    if not is_valid_git_diff(diff):
        return None

    # Limit size to prevent abuse (1MB)
    max_size = 1024 * 1024  # 1MB
    if len(diff) > max_size:
        # Truncate and add marker
        diff = diff[: max_size - 100] + "\n\n... [TRUNCATED - DIFF TOO LARGE] ..."

    return diff


logger = logging.getLogger(__name__)


def update_session_title_if_needed(
    db: Session,
    instance_id: UUID,
    user_message: str,
) -> None:
    """
    Update the session title if it's NULL by generating a title from the user message.

    This function:
    - Checks if the instance name is NULL
    - If NULL, generates a title using the LLM
    - Updates the instance name in the database
    - Handles errors gracefully

    Args:
        db: Database session
        instance_id: Agent instance ID
        user_message: The user's message content
    """
    try:
        # Import here to avoid circular imports
        from shared.database.models import AgentInstance
        from shared.llms import generate_conversation_title

        # Get the instance and check if name is already set
        instance = (
            db.query(AgentInstance).filter(AgentInstance.id == instance_id).first()
        )
        if not instance:
            logger.warning(f"Instance {instance_id} not found for title generation")
            return

        if instance.name is not None:
            logger.debug(
                f"Instance {instance_id} already has a name, skipping title generation"
            )
            return

        # Generate the title using the LLM utility
        title = generate_conversation_title(user_message)

        if title:
            instance.name = title
            db.commit()
            logger.info(f"Updated instance {instance_id} with title: {title}")
        else:
            logger.debug(f"No title generated for instance {instance_id}")

    except Exception as e:
        logger.error(
            f"Failed to update session title for instance {instance_id}: {str(e)}"
        )
        try:
            db.rollback()
        except Exception:
            pass
