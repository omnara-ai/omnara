#!/usr/bin/env python3
"""
Git utilities for tracking changes during agent sessions.

This module provides utilities for capturing git diffs to track code changes
made by AI agents during their sessions.
"""

import logging
import os
import subprocess
import time
from typing import Optional


class GitDiffTracker:
    """Tracks git changes from an initial state through a session."""

    def __init__(
        self,
        enabled: bool = True,
        logger: Optional[logging.Logger] = None,
        cwd: Optional[str] = None,
    ):
        """Initialize the git diff tracker.

        Args:
            enabled: Whether to enable git diff tracking (default: True)
            logger: Optional logger instance to use for logging. If not provided,
                    creates a default logger for this module.
            cwd: Working directory for git commands (default: current directory)
        """
        self.enabled = enabled
        self.cwd = cwd  # Store the working directory
        self.initial_git_hash: Optional[str] = None
        self.session_start_time = (
            time.time()
        )  # Track when session started for file filtering
        self.logger = logger or logging.getLogger(__name__)

        if self.enabled:
            self._capture_initial_state()

    def _capture_initial_state(self) -> None:
        """Capture the initial git commit hash if in a git repository."""
        try:
            result = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                capture_output=True,
                text=True,
                timeout=5,
                cwd=self.cwd,
            )
            if result.returncode == 0 and result.stdout.strip():
                self.initial_git_hash = result.stdout.strip()
                self.logger.info(
                    f"Git diff tracking enabled. Initial commit: {self.initial_git_hash[:8]}"
                )
            else:
                self.enabled = False
                self.logger.info(
                    "Not in a git repository or no commits found. Git diff tracking disabled."
                )
        except subprocess.TimeoutExpired:
            self.enabled = False
            self.logger.warning("Git command timed out. Git diff tracking disabled.")
        except Exception as e:
            self.enabled = False
            self.logger.warning(f"Failed to initialize git tracking: {e}")

    def get_diff(self) -> Optional[str]:
        """Get the current git diff from the initial state.

        Returns:
            The git diff output if enabled and there are changes, None otherwise.
        """
        if not self.enabled:
            return None

        try:
            combined_output = ""

            # Get list of worktrees to exclude
            exclude_patterns = self._get_worktree_exclusions()

            # Build git diff command
            if self.initial_git_hash:
                # Use git diff from initial hash to current working tree
                # This shows ALL changes (committed + uncommitted) as one unified diff
                diff_cmd = ["git", "diff", self.initial_git_hash]
            else:
                # No initial hash - just show uncommitted changes
                diff_cmd = ["git", "diff", "HEAD"]

            if exclude_patterns:
                diff_cmd.extend(["--"] + exclude_patterns)

            # Run git diff
            result = subprocess.run(
                diff_cmd, capture_output=True, text=True, timeout=5, cwd=self.cwd
            )
            if result.returncode == 0 and result.stdout.strip():
                combined_output = result.stdout.strip()

            # Get untracked files
            untracked_output = self._get_untracked_files(exclude_patterns)
            if untracked_output:
                if combined_output:
                    combined_output += "\n"
                combined_output += untracked_output

            # Return empty string if no changes (not None)
            # This allows the caller to distinguish between "no changes" (empty string)
            # and "diff tracking disabled" (None)
            return combined_output

        except subprocess.TimeoutExpired:
            self.logger.warning("Git diff command timed out")
            return None
        except Exception as e:
            self.logger.warning(f"Failed to get git diff: {e}")
            return None

    def _get_worktree_exclusions(self) -> list[str]:
        """Get list of worktree paths to exclude from diff.

        Returns:
            List of exclusion patterns for git commands.
        """
        exclude_patterns = []
        try:
            worktree_result = subprocess.run(
                ["git", "worktree", "list", "--porcelain"],
                capture_output=True,
                text=True,
                timeout=5,
                cwd=self.cwd,
            )
            if worktree_result.returncode == 0:
                # Parse worktree list to get paths to exclude
                current_dir = self.cwd or os.getcwd()
                for line in worktree_result.stdout.strip().split("\n"):
                    if line.startswith("worktree "):
                        worktree_path = line[9:]  # Remove "worktree " prefix
                        # Only exclude if it's a subdirectory of current directory
                        if worktree_path != current_dir and worktree_path.startswith(
                            os.path.dirname(current_dir)
                        ):
                            # Get relative path from current directory
                            try:
                                rel_path = os.path.relpath(worktree_path, current_dir)
                                if not rel_path.startswith(".."):
                                    exclude_patterns.append(f":(exclude){rel_path}")
                            except ValueError:
                                # Can't compute relative path, skip
                                pass
        except Exception:
            # Ignore worktree errors
            pass

        return exclude_patterns

    def _get_untracked_files(self, exclude_patterns: list[str]) -> str:
        """Get untracked files formatted as git diff output.

        Args:
            exclude_patterns: List of patterns to exclude from the output.

        Returns:
            Formatted diff-like output for untracked files.
        """
        output = ""
        try:
            # Get untracked files (with exclusions)
            untracked_cmd = ["git", "ls-files", "--others", "--exclude-standard"]
            if exclude_patterns:
                untracked_cmd.extend(["--"] + exclude_patterns)

            result = subprocess.run(
                untracked_cmd, capture_output=True, text=True, timeout=5, cwd=self.cwd
            )
            if result.returncode == 0 and result.stdout.strip():
                untracked_files = result.stdout.strip().split("\n")

                # For each untracked file, show its contents with diff-like format
                for file_path in untracked_files:
                    # Check if file was created after session started
                    try:
                        # Get absolute path for file operations
                        abs_file_path = os.path.join(self.cwd or os.getcwd(), file_path)
                        file_creation_time = os.path.getctime(abs_file_path)
                        if file_creation_time < self.session_start_time:
                            # Skip files that existed before the session started
                            continue
                    except (OSError, IOError):
                        # If we can't get creation time, skip the file
                        continue

                    output += f"diff --git a/{file_path} b/{file_path}\n"
                    output += "new file mode 100644\n"
                    output += "index 0000000..0000000\n"
                    output += "--- /dev/null\n"
                    output += f"+++ b/{file_path}\n"

                    # Read file contents and add with + prefix
                    try:
                        with open(
                            abs_file_path, "r", encoding="utf-8", errors="ignore"
                        ) as f:
                            lines = f.readlines()
                            output += f"@@ -0,0 +1,{len(lines)} @@\n"
                            for line in lines:
                                # Preserve the line exactly as-is, just add + prefix
                                if line.endswith("\n"):
                                    output += f"+{line}"
                                else:
                                    output += f"+{line}\n"
                            if lines and not lines[-1].endswith("\n"):
                                output += "\n\\ No newline at end of file\n"
                    except Exception:
                        output += "@@ -0,0 +1,1 @@\n"
                        output += "+[Binary or unreadable file]\n"

                    output += "\n"
        except Exception:
            # Ignore errors getting untracked files
            pass

        return output
