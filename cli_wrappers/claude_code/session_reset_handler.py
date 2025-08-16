"""Handler for Claude session resets (/clear and /reset commands)"""

import time
from pathlib import Path
from typing import Optional, Tuple


class SessionResetHandler:
    """Handles detection and recovery from Claude session resets"""

    def __init__(self, log_func=None):
        """Initialize the handler

        Args:
            log_func: Optional logging function
        """
        self.log = log_func or print
        self.reset_pending = False
        self.reset_command = None
        self.reset_time = None

    def check_for_reset_command(self, command: str) -> bool:
        """Check if a command is a session reset command"""
        return command.lower() in ["/clear", "/reset"]

    def mark_reset_detected(self, command: str) -> None:
        """Mark that a session reset has been detected"""
        self.reset_pending = True
        self.reset_command = command
        self.reset_time = time.time()
        self.log(
            f"[STDIN] 🔄 Session reset detected: {command} - will switch to new JSONL file"
        )

    def is_reset_pending(self) -> bool:
        """Check if a session reset is pending"""
        return self.reset_pending

    def clear_reset_state(self) -> None:
        """Clear the reset state after handling"""
        self.reset_pending = False
        self.reset_command = None
        self.reset_time = None

    def get_reset_info(self) -> Tuple[Optional[str], Optional[float]]:
        """Get information about the pending reset"""
        if self.reset_pending:
            return self.reset_command, self.reset_time
        return None, None

    def find_reset_session_file(
        self, project_dir: Path, current_file: Path, max_wait: float = 10.0
    ) -> Optional[Path]:
        """Find a new session file created after a reset

        Looks for JSONL files created after the reset time that contain
        <command-name>/clear</command-name> in the first few lines.

        Args:
            project_dir: Directory to search in
            current_file: Current JSONL file being monitored
            max_wait: Maximum time to wait in seconds

        Returns:
            Path to new JSONL file if found, None otherwise
        """
        if not self.reset_pending or not self.reset_time:
            return None

        if not project_dir or not project_dir.exists():
            return None

        start_time = time.time()

        while time.time() - start_time < max_wait:
            try:
                # Get all JSONL files created after reset
                jsonl_files = [
                    f
                    for f in project_dir.glob("*.jsonl")
                    if f.stat().st_mtime > self.reset_time and f != current_file
                ]

                # Sort by modification time to check newest first
                jsonl_files.sort(key=lambda f: f.stat().st_mtime, reverse=True)

                for file in jsonl_files:
                    # Check if this file contains the /clear command
                    if self._file_has_clear_command(file):
                        self.log(f"[INFO] Found reset session file: {file.name}")
                        return file

            except Exception as e:
                self.log(f"[ERROR] Error searching for reset file: {e}")

            time.sleep(0.5)

        self.log(f"[WARNING] No reset session file found after {max_wait}s")
        return None

    def _file_has_clear_command(self, file: Path) -> bool:
        """Check if a JSONL file starts with the /clear command"""
        try:
            with open(file, "r") as f:
                # Check first few lines for the exact /clear command structure
                for i, line in enumerate(f):
                    if i > 5:  # Only check first few lines
                        break
                    # Look for the exact command structure
                    if "<command-name>/clear</command-name>" in line:
                        return True
        except Exception:
            pass
        return False
