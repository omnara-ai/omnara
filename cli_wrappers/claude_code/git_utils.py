"""Git diff utilities for Claude wrapper"""

import os
import subprocess
import time
from typing import Optional, Tuple


class GitDiffManager:
    """Manages git diff functionality for the Claude wrapper"""

    def __init__(self, log_func=None, session_start_time=None):
        """Initialize the GitDiffManager
        
        Args:
            log_func: Optional logging function
            session_start_time: Optional session start time (defaults to current time)
        """
        self.log = log_func or (lambda x: None)
        self.git_diff_enabled = False
        self.initial_git_hash = None
        self.session_start_time = session_start_time or time.time()

    def initialize_git(self) -> Tuple[bool, Optional[str]]:
        """Auto-detect git repository and enable git diff if available
        
        Returns:
            Tuple of (enabled, initial_hash)
        """
        try:
            result = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                capture_output=True,
                text=True,
                timeout=5,
            )
            if result.returncode == 0 and result.stdout.strip():
                self.initial_git_hash = result.stdout.strip()
                self.git_diff_enabled = True
                self.log(
                    f"[INFO] Git diff enabled. Initial commit: {self.initial_git_hash[:8]}"
                )
                return True, self.initial_git_hash
            else:
                self.git_diff_enabled = False
                self.log("[INFO] Git diff disabled (not in a git repository)")
                return False, None
        except (subprocess.TimeoutExpired, FileNotFoundError, Exception) as e:
            self.git_diff_enabled = False
            self.log(
                f"[INFO] Git diff disabled (git not available or error: {e})"
            )
            return False, None

    def get_git_diff(self) -> Optional[str]:
        """Get the current git diff if enabled.

        Returns:
            The git diff output if enabled and there are changes, None otherwise.
        """
        # Check if git diff is enabled
        if not self.git_diff_enabled:
            return None

        try:
            combined_output = ""

            # Get list of worktrees to exclude
            worktree_result = subprocess.run(
                ["git", "worktree", "list", "--porcelain"],
                capture_output=True,
                text=True,
                timeout=5,
            )
            exclude_patterns = []
            if worktree_result.returncode == 0:
                # Parse worktree list to get paths to exclude
                cwd = os.getcwd()
                for line in worktree_result.stdout.strip().split("\n"):
                    if line.startswith("worktree "):
                        worktree_path = line[9:]  # Remove "worktree " prefix
                        # Only exclude if it's a subdirectory of current directory
                        if worktree_path != cwd and worktree_path.startswith(
                            os.path.dirname(cwd)
                        ):
                            # Get relative path from current directory
                            try:
                                rel_path = os.path.relpath(worktree_path, cwd)
                                if not rel_path.startswith(".."):
                                    exclude_patterns.append(f":(exclude){rel_path}")
                            except ValueError:
                                # Can't compute relative path, skip
                                pass

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
            result = subprocess.run(diff_cmd, capture_output=True, text=True, timeout=5)
            if result.returncode == 0 and result.stdout.strip():
                combined_output = result.stdout.strip()

            # Get untracked files (with exclusions)
            untracked_cmd = ["git", "ls-files", "--others", "--exclude-standard"]
            if exclude_patterns:
                untracked_cmd.extend(["--"] + exclude_patterns)

            result_untracked = subprocess.run(
                untracked_cmd, capture_output=True, text=True, timeout=5
            )
            if result_untracked.returncode == 0 and result_untracked.stdout.strip():
                untracked_files = result_untracked.stdout.strip().split("\n")
                if untracked_files:
                    if combined_output:
                        combined_output += "\n"

                    # For each untracked file, show its contents with diff-like format
                    for file_path in untracked_files:
                        # Check if file was created after session started
                        try:
                            file_creation_time = os.path.getctime(file_path)
                            if file_creation_time < self.session_start_time:
                                # Skip files that existed before the session started
                                continue
                        except (OSError, IOError):
                            # If we can't get creation time, skip the file
                            continue

                        combined_output += f"diff --git a/{file_path} b/{file_path}\n"
                        combined_output += "new file mode 100644\n"
                        combined_output += "index 0000000..0000000\n"
                        combined_output += "--- /dev/null\n"
                        combined_output += f"+++ b/{file_path}\n"

                        # Read file contents and add with + prefix
                        try:
                            with open(
                                file_path, "r", encoding="utf-8", errors="ignore"
                            ) as f:
                                lines = f.readlines()
                                combined_output += f"@@ -0,0 +1,{len(lines)} @@\n"
                                for line in lines:
                                    # Preserve the line exactly as-is, just add + prefix
                                    if line.endswith("\n"):
                                        combined_output += f"+{line}"
                                    else:
                                        combined_output += f"+{line}\n"
                                if lines and not lines[-1].endswith("\n"):
                                    combined_output += "\\ No newline at end of file\n"
                        except Exception:
                            combined_output += "@@ -0,0 +1,1 @@\n"
                            combined_output += "+[Binary or unreadable file]\n"

                        combined_output += "\n"

            return combined_output

        except Exception as e:
            self.log(f"[WARNING] Failed to get git diff: {e}")

        return None