#!/usr/bin/env python3
"""
Unit tests for AmpWrapper components
Tests individual methods and classes in isolation
"""

import json
import os
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

# Skip all tests in this module when running in CI
if os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true":
    import pytest

    pytestmark = pytest.mark.skip(
        reason="AMP wrapper tests disabled in CI due to file descriptor issues"
    )

from integrations.cli_wrappers.amp.amp import (
    AmpWrapper,
    MessageProcessor,
    ANSI_ESCAPE,
)


class TestAmpWrapperInit(unittest.TestCase):
    """Test AmpWrapper initialization"""

    def test_init_with_api_key(self):
        """Test constructor with API key"""
        wrapper = AmpWrapper(api_key="test_key", base_url="http://test.com")

        self.assertEqual(wrapper.api_key, "test_key")
        self.assertEqual(wrapper.base_url, "http://test.com")
        self.assertIsNotNone(wrapper.session_uuid)
        self.assertIsInstance(wrapper.session_start_time, float)
        self.assertIsNone(wrapper.agent_instance_id)

    def test_init_without_api_key_with_env(self):
        """Test constructor with API key from environment"""
        with patch.dict(os.environ, {"OMNARA_API_KEY": "env_key"}):
            wrapper = AmpWrapper()
            self.assertEqual(wrapper.api_key, "env_key")

    def test_init_without_api_key_fails(self):
        """Test constructor fails without API key"""
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(SystemExit):
                AmpWrapper()


class TestANSIProcessing(unittest.TestCase):
    """Test ANSI escape code processing"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_strip_ansi_simple(self):
        """Test basic ANSI code stripping"""
        text_with_ansi = "\x1b[31mHello\x1b[0m World"
        expected = "Hello World"
        result = self.wrapper.strip_ansi(text_with_ansi)
        self.assertEqual(result, expected)

    def test_strip_ansi_complex(self):
        """Test complex ANSI codes"""
        text_with_ansi = "\x1b[2mThinking...\x1b[0m\n\x1b[90mThis is dim text\x1b[0m"
        expected = "Thinking...\nThis is dim text"
        result = self.wrapper.strip_ansi(text_with_ansi)
        self.assertEqual(result, expected)

    def test_strip_ansi_no_codes(self):
        """Test text without ANSI codes"""
        text = "Plain text"
        result = self.wrapper.strip_ansi(text)
        self.assertEqual(result, text)

    def test_ansi_regex_compiled(self):
        """Test that ANSI regex is properly compiled"""
        self.assertTrue(hasattr(ANSI_ESCAPE, "sub"))


class TestAmpCLILocation(unittest.TestCase):
    """Test Amp CLI binary location"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    @patch("shutil.which")
    def test_find_amp_cli_in_path(self, mock_which):
        """Test finding Amp in PATH"""
        mock_which.return_value = "/usr/bin/amp"
        result = self.wrapper.find_amp_cli()
        self.assertEqual(result, "/usr/bin/amp")
        mock_which.assert_called_once_with("amp")

    @patch("shutil.which")
    @patch("pathlib.Path.exists")
    @patch("pathlib.Path.is_file")
    def test_find_amp_cli_in_common_locations(
        self, mock_is_file, mock_exists, mock_which
    ):
        """Test finding Amp in common installation locations"""
        mock_which.return_value = None
        mock_exists.side_effect = lambda: True  # First location exists
        mock_is_file.side_effect = lambda: True  # And it's a file

        result = self.wrapper.find_amp_cli()
        self.assertTrue(result.endswith("amp"))

    @patch("shutil.which")
    @patch("pathlib.Path.exists")
    def test_find_amp_cli_not_found(self, mock_exists, mock_which):
        """Test behavior when Amp CLI is not found"""
        mock_which.return_value = None
        mock_exists.return_value = False

        with self.assertRaises(FileNotFoundError):
            self.wrapper.find_amp_cli()


class TestAmpSettings(unittest.TestCase):
    """Test Amp settings file creation"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_create_amp_settings(self):
        """Test settings file creation"""
        settings_file = self.wrapper.create_amp_settings()

        # Verify file exists
        self.assertTrue(Path(settings_file).exists())

        # Verify content
        with open(settings_file, "r") as f:
            settings = json.load(f)

        self.assertIn("amp.mcpServers", settings)
        self.assertIn("amp.commands.allowlist", settings)
        self.assertEqual(settings["amp.commands.allowlist"], ["*"])

        # Clean up
        Path(settings_file).unlink()


class TestGitTracking(unittest.TestCase):
    """Test git repository tracking"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    @patch("subprocess.run")
    def test_init_git_tracking_success(self, mock_run):
        """Test successful git tracking initialization"""
        # Mock git commands
        mock_run.side_effect = [
            Mock(returncode=0, stdout="/path/to/repo"),  # git rev-parse --show-toplevel
            Mock(returncode=0, stdout="abc123\n"),  # git rev-parse HEAD
        ]

        self.wrapper.init_git_tracking()

        self.assertTrue(self.wrapper.git_diff_enabled)
        self.assertEqual(self.wrapper.initial_git_hash, "abc123")

    @patch("subprocess.run")
    def test_init_git_tracking_no_repo(self, mock_run):
        """Test git tracking when not in a repo"""
        mock_run.side_effect = [
            Mock(returncode=128)  # git rev-parse --show-toplevel fails
        ]

        self.wrapper.init_git_tracking()

        self.assertFalse(self.wrapper.git_diff_enabled)
        self.assertIsNone(self.wrapper.initial_git_hash)

    @patch("subprocess.run")
    def test_init_git_tracking_no_commits(self, mock_run):
        """Test git tracking with no commits"""
        mock_run.side_effect = [
            Mock(returncode=0, stdout="/path/to/repo"),  # git rev-parse --show-toplevel
            Mock(returncode=128),  # git rev-parse HEAD fails (no commits)
        ]

        self.wrapper.init_git_tracking()

        self.assertTrue(self.wrapper.git_diff_enabled)
        self.assertEqual(
            self.wrapper.initial_git_hash, "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
        )  # Empty tree hash

    @patch("subprocess.run")
    def test_get_git_diff_success(self, mock_run):
        """Test successful git diff generation"""
        self.wrapper.git_diff_enabled = True
        self.wrapper.initial_git_hash = "abc123"

        mock_run.side_effect = [
            Mock(
                returncode=0, stdout="diff --git a/test.py b/test.py\n+print('hello')"
            ),
            Mock(returncode=0, stdout="?? new_file.txt\n"),
        ]

        diff = self.wrapper.get_git_diff()

        self.assertIsNotNone(diff)
        if diff is not None:
            self.assertIn("diff --git a/test.py b/test.py", diff)
            self.assertIn("diff --git a/new_file.txt b/new_file.txt", diff)

    def test_get_git_diff_disabled(self):
        """Test git diff when tracking disabled"""
        self.wrapper.git_diff_enabled = False

        diff = self.wrapper.get_git_diff()

        self.assertIsNone(diff)


class TestMessageProcessor(unittest.TestCase):
    """Test MessageProcessor class"""

    def setUp(self):
        self.wrapper_mock = Mock()
        self.wrapper_mock.log = Mock()
        self.wrapper_mock.agent_instance_id = "test_instance"
        self.wrapper_mock.omnara_client_sync = Mock()
        self.wrapper_mock.get_git_diff = Mock(return_value=None)
        self.wrapper_mock.input_queue = []

        self.processor = MessageProcessor(self.wrapper_mock)

    def test_process_user_message_from_web(self):
        """Test processing user message from web UI"""
        content = "Test message from web"

        self.processor.process_user_message_sync(content, from_web=True)

        # Should be added to web UI messages set
        self.assertIn(content, self.processor.web_ui_messages)
        # Should not be sent to API (from_web=True)
        self.wrapper_mock.omnara_client_sync.send_user_message.assert_not_called()

    def test_process_user_message_from_cli_new(self):
        """Test processing new user message from CLI"""
        content = "Test message from CLI"

        self.processor.process_user_message_sync(content, from_web=False)

        # Should be sent to API
        self.wrapper_mock.omnara_client_sync.send_user_message.assert_called_once_with(
            agent_instance_id="test_instance", content=content
        )

    def test_process_user_message_from_cli_duplicate(self):
        """Test processing duplicate message from CLI (already from web)"""
        content = "Test duplicate message"
        self.processor.web_ui_messages.add(content)

        self.processor.process_user_message_sync(content, from_web=False)

        # Should not be sent to API (duplicate)
        self.wrapper_mock.omnara_client_sync.send_user_message.assert_not_called()
        # Should be removed from web messages set
        self.assertNotIn(content, self.processor.web_ui_messages)

    def test_process_assistant_message_sync(self):
        """Test processing assistant message"""
        content = "Assistant response"
        mock_response = Mock()
        mock_response.message_id = "msg_123"
        mock_response.agent_instance_id = "instance_123"
        mock_response.queued_user_messages = ["Queued message"]

        self.wrapper_mock.omnara_client_sync.send_message.return_value = mock_response

        self.processor.process_assistant_message_sync(content)

        # Should send message to API
        self.wrapper_mock.omnara_client_sync.send_message.assert_called_once()
        call_args = self.wrapper_mock.omnara_client_sync.send_message.call_args
        self.assertEqual(call_args[1]["content"], content)
        self.assertEqual(call_args[1]["agent_type"], "Amp")

        # Should update message tracking
        self.assertEqual(self.processor.last_message_id, "msg_123")

        # Should process queued messages
        self.assertIn("Queued message", self.processor.web_ui_messages)
        self.assertIn("Queued message", self.wrapper_mock.input_queue)

    def test_should_request_input(self):
        """Test input request logic"""
        self.processor.last_message_id = "msg_123"
        self.processor.pending_input_message_id = None
        self.wrapper_mock.is_amp_idle.return_value = True

        result = self.processor.should_request_input()

        self.assertEqual(result, "msg_123")

    def test_should_not_request_input_already_pending(self):
        """Test input request logic when already pending"""
        self.processor.last_message_id = "msg_123"
        self.processor.pending_input_message_id = "msg_123"
        self.wrapper_mock.is_amp_idle.return_value = True

        result = self.processor.should_request_input()

        self.assertIsNone(result)

    def test_should_not_request_input_not_idle(self):
        """Test input request logic when Amp not idle"""
        self.processor.last_message_id = "msg_123"
        self.processor.pending_input_message_id = None
        self.wrapper_mock.is_amp_idle.return_value = False

        result = self.processor.should_request_input()

        self.assertIsNone(result)


class TestAmpIdleDetection(unittest.TestCase):
    """Test Amp idle state detection"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_is_amp_idle_waiting_for_response(self):
        """Test idle detection when waiting for response"""
        self.wrapper.waiting_for_response = True
        self.wrapper.terminal_buffer = "╭─ prompt box"

        result = self.wrapper.is_amp_idle()

        self.assertFalse(result)

    def test_is_amp_idle_processing(self):
        """Test idle detection when processing"""
        self.wrapper.waiting_for_response = False
        self.wrapper.terminal_buffer = "Running inference..."

        result = self.wrapper.is_amp_idle()

        self.assertFalse(result)

    def test_is_amp_idle_prompt_ready(self):
        """Test idle detection with prompt box visible"""
        self.wrapper.waiting_for_response = False
        self.wrapper.terminal_buffer = "╭─ prompt ready"

        result = self.wrapper.is_amp_idle()

        self.assertTrue(result)

    def test_is_amp_idle_timeout(self):
        """Test idle detection after timeout"""
        self.wrapper.waiting_for_response = False
        self.wrapper.terminal_buffer = "some output"
        self.wrapper.last_output_time = 0  # Very old timestamp

        result = self.wrapper.is_amp_idle()

        self.assertTrue(result)


if __name__ == "__main__":
    unittest.main()
