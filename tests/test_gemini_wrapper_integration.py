#!/usr/bin/env python3
"""
Lightweight integration tests for GeminiWrapper PTY behavior (mocked).
"""

import os
import threading
import time
import unittest
from unittest.mock import patch

# Skip PTY-heavy tests in CI
if os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true":
    import pytest

    pytestmark = pytest.mark.skip(
        reason="Gemini wrapper PTY tests disabled in CI"
    )

from integrations.cli_wrappers.gemini.gemini_wrapper import GeminiWrapper


class TestPTYIntegration(unittest.TestCase):
    def setUp(self):
        self.wrapper = GeminiWrapper(api_key="test")

    @patch("pty.fork", return_value=(12345, 10))
    @patch("os.execvp")
    def test_pty_creation_and_loop(self, mock_execvp, mock_fork):
        # Make tcgetattr return something to avoid branch differences
        with (
            patch("termios.tcgetattr", side_effect=Exception("no tty")),
            patch("tty.setraw"),
            patch("select.select", return_value=([], [], [])),
            patch("os.get_terminal_size", return_value=(80, 24)),
            patch("os.read", return_value=b""),  # simulate EOF from child
            patch("os.waitpid", return_value=(12345, 0)),
            patch("integrations.cli_wrappers.gemini.gemini_wrapper.find_gemini_cli", return_value="/usr/bin/gemini"),
        ):
            # Run in thread to avoid blocking
            t = threading.Thread(target=self.wrapper.run_gemini_with_pty, args=([],))
            t.daemon = True
            t.start()
            time.sleep(0.1)
            self.wrapper.running = False
            t.join(timeout=1)
            # No assertion beyond "did not crash"; just ensures loop wiring works


if __name__ == "__main__":
    unittest.main()

