"""Database utility functions."""

import re
from typing import Optional


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
        diff: The git diff string to sanitize

    Returns:
        The original diff if valid, None if invalid
    """
    if not diff:
        return None

    # Strip excessive whitespace
    diff = diff.strip()

    # Check if it's a valid git diff
    if not is_valid_git_diff(diff):
        return None

    # Limit size to prevent abuse (1MB)
    max_size = 1024 * 1024  # 1MB
    if len(diff) > max_size:
        # Truncate and add marker
        diff = diff[: max_size - 100] + "\n\n... [TRUNCATED - DIFF TOO LARGE] ..."

    return diff
