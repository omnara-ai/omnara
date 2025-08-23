#!/usr/bin/env python3
"""
Formatting utilities for Claude Code wrapper.

This module contains stateless formatting functions used to format tool usage,
content blocks, and other output for display in the terminal.
"""

import json
from typing import Any, Dict, Optional


def truncate_text(text: str, max_length: int = 100) -> str:
    """Truncate text to max length with ellipsis if needed.

    Args:
        text: The text to truncate
        max_length: Maximum length before truncation

    Returns:
        The truncated text with ellipsis if it was truncated
    """
    if len(text) <= max_length:
        return text
    return text[:max_length] + "..."


def format_tool_usage(tool_name: str, input_data: Dict[str, Any]) -> str:
    """Format tool usage information based on tool type with markdown.

    Args:
        tool_name: Name of the tool being used
        input_data: Input data passed to the tool

    Returns:
        Formatted string describing the tool usage
    """
    # Skip MCP omnara tools - just show tool name
    if tool_name.startswith("mcp__omnara__"):
        return f"Using tool: {tool_name}"

    # Write tool - show content in code block
    if tool_name == "Write":
        file_path = input_data.get("file_path", "unknown")
        content = input_data.get("content", "")

        # Detect file type for syntax highlighting
        file_ext = file_path.split(".")[-1] if "." in file_path else ""
        lang_map = {
            "py": "python",
            "js": "javascript",
            "ts": "typescript",
            "jsx": "jsx",
            "tsx": "tsx",
            "java": "java",
            "cpp": "cpp",
            "c": "c",
            "cs": "csharp",
            "rb": "ruby",
            "go": "go",
            "rs": "rust",
            "php": "php",
            "swift": "swift",
            "kt": "kotlin",
            "yaml": "yaml",
            "yml": "yaml",
            "json": "json",
            "xml": "xml",
            "html": "html",
            "css": "css",
            "scss": "scss",
            "sql": "sql",
            "sh": "bash",
            "bash": "bash",
            "md": "markdown",
            "txt": "text",
        }
        lang = lang_map.get(file_ext, "")

        lines = [f"Using tool: Write - `{file_path}`"]
        lines.append(f"```{lang}")
        lines.append(content)
        lines.append("```")
        return "\n".join(lines)

    # Other file-related tools
    elif tool_name in ["Read", "NotebookRead", "NotebookEdit"]:
        file_path = input_data.get(
            "file_path", input_data.get("notebook_path", "unknown")
        )
        return f"Using tool: {tool_name} - `{file_path}`"

    # Edit tool - show full diff without truncation
    elif tool_name == "Edit":
        file_path = input_data.get("file_path", "unknown")
        old_string = input_data.get("old_string", "")
        new_string = input_data.get("new_string", "")
        replace_all = input_data.get("replace_all", False)

        # Create a markdown diff
        diff_lines = []
        diff_lines.append(f"Using tool: **Edit** - `{file_path}`")

        if replace_all:
            diff_lines.append("*Replacing all occurrences*")

        diff_lines.append("")

        # Handle empty old_string (new content)
        if not old_string and new_string:
            # Adding new content
            diff_lines.append("```diff")
            for line in new_string.splitlines():
                diff_lines.append(f"+ {line}")
            diff_lines.append("```")
        # Handle empty new_string (deletion)
        elif old_string and not new_string:
            # Removing content
            diff_lines.append("```diff")
            for line in old_string.splitlines():
                diff_lines.append(f"- {line}")
            diff_lines.append("```")
        # Handle replacement - try to show as inline diff if possible
        elif old_string and new_string:
            old_lines = old_string.splitlines()
            new_lines = new_string.splitlines()

            # Try to find the actual change within context
            # Look for common prefix and suffix
            common_prefix = []
            common_suffix = []

            # Find common prefix
            for i in range(min(len(old_lines), len(new_lines))):
                if old_lines[i] == new_lines[i]:
                    common_prefix.append(old_lines[i])
                else:
                    break

            # Find common suffix
            old_remaining = old_lines[len(common_prefix) :]
            new_remaining = new_lines[len(common_prefix) :]

            if old_remaining and new_remaining:
                for i in range(1, min(len(old_remaining), len(new_remaining)) + 1):
                    if old_remaining[-i] == new_remaining[-i]:
                        common_suffix.insert(0, old_remaining[-i])
                    else:
                        break

            # Get the actual changed lines
            changed_old = (
                old_remaining[: len(old_remaining) - len(common_suffix)]
                if common_suffix
                else old_remaining
            )
            changed_new = (
                new_remaining[: len(new_remaining) - len(common_suffix)]
                if common_suffix
                else new_remaining
            )

            # If we have context and a focused change, show it inline style
            if (common_prefix or common_suffix) and (changed_old or changed_new):
                diff_lines.append("```diff")

                # Show some context before (last 2 lines of prefix)
                context_before = (
                    common_prefix[-2:] if len(common_prefix) > 2 else common_prefix
                )
                for line in context_before:
                    diff_lines.append(f"  {line}")

                # Show removed lines
                for line in changed_old:
                    diff_lines.append(f"- {line}")

                # Show added lines
                for line in changed_new:
                    diff_lines.append(f"+ {line}")

                # Show some context after (first 2 lines of suffix)
                context_after = (
                    common_suffix[:2] if len(common_suffix) > 2 else common_suffix
                )
                for line in context_after:
                    diff_lines.append(f"  {line}")

                diff_lines.append("```")
            else:
                # Full replacement - no common context
                diff_lines.append("```diff")
                for line in old_lines:
                    diff_lines.append(f"- {line}")
                for line in new_lines:
                    diff_lines.append(f"+ {line}")
                diff_lines.append("```")

        return "\n".join(diff_lines)

    # MultiEdit tool - show file path and all edits with full diffs
    elif tool_name == "MultiEdit":
        file_path = input_data.get("file_path", "unknown")
        edits = input_data.get("edits", [])

        lines = [f"Using tool: **MultiEdit** - `{file_path}`"]
        lines.append(f"*Making {len(edits)} edit{'s' if len(edits) != 1 else ''}:*")
        lines.append("")

        # Show each edit with full content (no truncation)
        for i, edit in enumerate(edits, 1):
            old_string = edit.get("old_string", "")
            new_string = edit.get("new_string", "")
            replace_all = edit.get("replace_all", False)

            # Add edit header
            if replace_all:
                lines.append(f"### Edit {i} *(replacing all occurrences)*")
            else:
                lines.append(f"### Edit {i}")

            lines.append("")

            # Create a proper diff display
            lines.append("```diff")

            # Handle empty old_string (new content)
            if not old_string and new_string:
                # Adding new content
                for line in new_string.splitlines():
                    lines.append(f"+ {line}")
            # Handle empty new_string (deletion)
            elif old_string and not new_string:
                # Removing content
                for line in old_string.splitlines():
                    lines.append(f"- {line}")
            # Handle replacement
            elif old_string and new_string:
                # Show the removal first
                for line in old_string.splitlines():
                    lines.append(f"- {line}")
                # Then show the addition
                for line in new_string.splitlines():
                    lines.append(f"+ {line}")

            lines.append("```")
            lines.append("")  # Add spacing between edits

        return "\n".join(lines)

    # Command execution
    elif tool_name == "Bash":
        command = input_data.get("command", "")
        return f"Using tool: Bash - `{command}`"

    # Search tools
    elif tool_name in ["Grep", "Glob"]:
        pattern = input_data.get("pattern", "unknown")
        path = input_data.get("path", "current directory")
        return f"Using tool: {tool_name} - `{truncate_text(pattern, 50)}` in {path}"

    # Directory listing
    elif tool_name == "LS":
        path = input_data.get("path", "unknown")
        return f"Using tool: LS - `{path}`"

    # Todo management
    elif tool_name == "TodoWrite":
        todos = input_data.get("todos", [])

        if not todos:
            return "Using tool: TodoWrite - clearing todo list"

        # Map status to symbols
        status_symbol = {"pending": "○", "in_progress": "◐", "completed": "●"}

        # Group todos by status for counting
        status_counts = {"pending": 0, "in_progress": 0, "completed": 0}

        # Build formatted todo list
        lines = ["Using tool: TodoWrite - Todo List", ""]

        for todo in todos:
            status = todo.get("status", "pending")
            content = todo.get("content", "")

            # Count by status
            if status in status_counts:
                status_counts[status] += 1

            # Truncate content if too long
            max_content_length = 100
            content_truncated = truncate_text(content, max_content_length)

            # Format todo item with symbol
            symbol = status_symbol.get(status, "•")
            lines.append(f"{symbol} {content_truncated}")

        return "\n".join(lines)

    # Task delegation
    elif tool_name == "Task":
        description = input_data.get("description", "unknown task")
        subagent_type = input_data.get("subagent_type", "unknown")
        return f"Using tool: Task - {truncate_text(description, 50)} (agent: {subagent_type})"

    # Web operations
    elif tool_name == "WebFetch":
        url = input_data.get("url", "unknown")
        return f"Using tool: WebFetch - `{truncate_text(url, 80)}`"

    elif tool_name == "WebSearch":
        query = input_data.get("query", "unknown")
        return f"Using tool: WebSearch - {truncate_text(query, 80)}"

    # MCP resource listing
    elif tool_name == "ListMcpResourcesTool":
        return "Using tool: List MCP Resources"

    # Default case for unknown tools
    else:
        # Try to extract meaningful info from input_data
        if input_data:
            # Look for common parameter names
            for key in [
                "file",
                "path",
                "query",
                "content",
                "message",
                "description",
                "name",
            ]:
                if key in input_data:
                    value = str(input_data[key])
                    return f"Using tool: {tool_name} - {truncate_text(value, 50)}"

        return f"Using tool: {tool_name}"


def format_content_block(block: Dict[str, Any]) -> Optional[str]:
    """Format different types of content blocks with markdown.

    Args:
        block: Content block dictionary with type and content

    Returns:
        Formatted string for the content block, or None if it should be skipped
    """
    block_type = block.get("type")

    if block_type == "text":
        text_content = block.get("text", "")
        if not text_content:
            return None
        return text_content

    elif block_type == "tool_use":
        # Track tool usage
        tool_name = block.get("name", "unknown")
        input_data = block.get("input", {})
        return format_tool_usage(tool_name, input_data)

    elif block_type == "tool_result":
        # Format tool results
        content = block.get("content", [])
        if isinstance(content, list):
            # Extract text from tool result content
            result_texts = []
            for item in content:
                if isinstance(item, dict) and item.get("type") == "text":
                    result_text = item.get("text", "")
                    if result_text:
                        # Try to parse as JSON for cleaner display
                        try:
                            parsed = json.loads(result_text)
                            # Just show a compact summary for JSON results
                            if isinstance(parsed, dict):
                                keys = list(parsed.keys())[:3]
                                summary = f"JSON object with keys: {', '.join(keys)}"
                                if len(parsed) > 3:
                                    summary += f" and {len(parsed) - 3} more"
                                result_texts.append(summary)
                            else:
                                result_texts.append(truncate_text(result_text, 100))
                        except (json.JSONDecodeError, ValueError):
                            # Not JSON, just add as text
                            result_texts.append(truncate_text(result_text, 100))
            if result_texts:
                combined = " | ".join(result_texts)
                return f"Result: {combined}"
        elif isinstance(content, str):
            return f"Result: {truncate_text(content, 200)}"
        return "Result: [empty]"

    elif block_type == "thinking":
        # Include thinking content
        thinking_text = block.get("text", "")
        if thinking_text:
            return f"[Thinking: {truncate_text(thinking_text, 200)}]"

    # Unknown block type
    return None
