#!/usr/bin/env python3
"""
Unit tests for GeminiWrapper components
Modeled after Amp wrapper unit tests, trimmed for minimal wrapper.
"""

import os
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

# Skip all tests in this module when running in CI (pty issues)
if os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true":
    import pytest

    pytestmark = pytest.mark.skip(
        reason="Gemini wrapper tests disabled in CI due to file descriptor issues"
    )

from integrations.cli_wrappers.gemini.gemini_wrapper import (
    GeminiWrapper,
    MessageProcessor,
    ANSI_ESCAPE,
    strip_ansi,
    find_gemini_cli,
)


class TestGeminiWrapperInit(unittest.TestCase):
    def test_init_with_api_key(self):
        w = GeminiWrapper(api_key="k", base_url="http://x")
        self.assertEqual(w.api_key, "k")
        self.assertEqual(w.base_url, "http://x")
        self.assertIsNotNone(w.agent_instance_id)

    def test_init_without_api_key_with_env(self):
        with patch.dict(os.environ, {"OMNARA_API_KEY": "env_k"}):
            w = GeminiWrapper()
            self.assertEqual(w.api_key, "env_k")

    def test_init_without_api_key_fails(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(SystemExit):
                GeminiWrapper()


class TestANSIProcessing(unittest.TestCase):
    def test_strip_ansi_simple(self):
        self.assertEqual(strip_ansi("\x1b[31mHi\x1b[0m"), "Hi")

    def test_ansi_regex_compiled(self):
        self.assertTrue(hasattr(ANSI_ESCAPE, "sub"))


class TestGeminiCLILocation(unittest.TestCase):
    @patch("shutil.which")
    def test_find_gemini_cli_in_path(self, mock_which):
        mock_which.return_value = "/usr/bin/gemini"
        self.assertEqual(find_gemini_cli(), "/usr/bin/gemini")

    @patch("pathlib.Path.exists")
    @patch("pathlib.Path.is_file")
    def test_find_gemini_cli_via_env(self, mock_is_file, mock_exists):
        mock_exists.return_value = True
        mock_is_file.return_value = True
        with patch.dict(os.environ, {"OMNARA_GEMINI_PATH": "/opt/gemini"}):
            p = find_gemini_cli()
            self.assertTrue(p.endswith("gemini"))

    @patch("shutil.which")
    @patch("pathlib.Path.exists")
    def test_find_gemini_cli_not_found(self, mock_exists, mock_which):
        mock_which.return_value = None
        mock_exists.return_value = False
        with self.assertRaises(FileNotFoundError):
            find_gemini_cli()


class TestMessageProcessor(unittest.TestCase):
    def setUp(self):
        self.wrapper = Mock()
        self.wrapper.agent_instance_id = "inst"
        self.wrapper.omnara_client = Mock()
        self.wrapper.git_tracker = None
        self.proc = MessageProcessor(self.wrapper)

    def test_process_user_message_from_web(self):
        self.proc.process_user_message_sync("hello", from_web=True)
        self.assertIn("hello", self.proc.web_ui_messages)

    def test_process_user_message_from_cli(self):
        self.proc.process_user_message_sync("hello", from_web=False)
        self.wrapper.omnara_client.send_user_message.assert_called_once()

    def test_process_assistant_message_sync(self):
        self.proc.process_assistant_message_sync("Assistant says hi")
        self.wrapper.omnara_client.send_message.assert_called_once()


if __name__ == "__main__":
    unittest.main()

