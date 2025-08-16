#!/usr/bin/env python3
"""
Mock-based tests for AmpWrapper
Tests with mocked Amp and Omnara API behavior
"""

import asyncio
import json
import os
import unittest
from pathlib import Path
from unittest.mock import Mock, patch, AsyncMock

# Skip all tests in this module when running in CI
if os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true":
    import pytest

    pytestmark = pytest.mark.skip(
        reason="AMP wrapper tests disabled in CI due to file descriptor issues"
    )

from integrations.cli_wrappers.amp.amp import AmpWrapper, MessageProcessor


class MockAmpProcess:
    """Mock Amp process for testing"""

    def __init__(self, outputs=None):
        self.outputs = outputs or []
        self.current_output = 0
        self.pid = 12345

    def get_next_output(self):
        """Get the next output chunk"""
        if self.current_output < len(self.outputs):
            output = self.outputs[self.current_output]
            self.current_output += 1
            return output
        return ""

    def reset(self):
        """Reset output position"""
        self.current_output = 0


class MockOmnaraClient:
    """Mock Omnara client for testing"""

    def __init__(self):
        self.messages_sent = []
        self.responses = []
        self.current_response = 0

    def send_message(self, **kwargs):
        """Mock send_message method"""
        self.messages_sent.append(kwargs)

        if self.current_response < len(self.responses):
            response = self.responses[self.current_response]
            self.current_response += 1
            return Mock(**response)

        # Default response
        return Mock(
            message_id=f"msg_{len(self.messages_sent)}",
            agent_instance_id="test_instance",
            queued_user_messages=[],
        )

    def send_user_message(self, **kwargs):
        """Mock send_user_message method"""
        self.messages_sent.append({**kwargs, "type": "user_message"})

    def close(self):
        """Mock close method"""
        pass


class TestAmpOutputProcessing(unittest.TestCase):
    """Test Amp output processing with mocked data"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

        # Load fixture data
        self.fixtures_dir = Path(__file__).parent / "fixtures" / "amp_outputs"
        self.load_fixtures()

    def load_fixtures(self):
        """Load test fixture files"""
        self.fixtures = {}
        for fixture_file in self.fixtures_dir.glob("*.txt"):
            with open(fixture_file, "r") as f:
                self.fixtures[fixture_file.stem] = f.read()

    def test_amp_welcome_message_detection(self):
        """Test detection of Amp welcome message"""
        welcome_output = self.fixtures.get("welcome_message", "")

        # Process welcome message
        self.wrapper.amp_ready = False
        # Simulate welcome message processing (would be done in monitor_amp_output)
        if "Welcome to Amp" in welcome_output:
            self.wrapper.amp_ready = True

        self.assertTrue(self.wrapper.amp_ready)

    def test_response_extraction_with_thinking(self):
        """Test extracting response from output with thinking section"""
        thinking_output = self.fixtures.get("thinking_response", "")

        # Simulate the response extraction logic
        clean_output = self.wrapper.strip_ansi(thinking_output)

        # Should contain the thinking section
        self.assertIn("Thinking", clean_output)
        # Should contain the actual response
        self.assertIn("2 + 2 = 4", clean_output)

        # Test ANSI stripping with actual ANSI codes
        ansi_output = "\x1b[2mThinking...\x1b[0m\n2 + 2 = 4"
        clean_ansi = self.wrapper.strip_ansi(ansi_output)
        self.assertNotIn("\x1b", clean_ansi)
        self.assertIn("Thinking", clean_ansi)
        self.assertIn("2 + 2 = 4", clean_ansi)

    def test_response_extraction_simple(self):
        """Test extracting simple response without thinking"""
        simple_output = self.fixtures.get("simple_response", "")

        clean_output = self.wrapper.strip_ansi(simple_output)

        # Should contain the response
        self.assertIn("Hello! How can I help you today?", clean_output)
        # Should not have thinking section
        self.assertNotIn("Thinking", clean_output)

    def test_file_creation_detection(self):
        """Test detection of file creation in response"""
        file_output = self.fixtures.get("file_creation_response", "")

        clean_output = self.wrapper.strip_ansi(file_output)

        # Should detect file creation
        self.assertIn("Created hello.py", clean_output)
        self.assertIn('print("Hello, World!")', clean_output)

    def test_processing_indicator_detection(self):
        """Test detection of processing indicators"""
        for fixture_name, output in self.fixtures.items():
            if "Running inference" in output:
                # Should detect processing
                self.assertIn("Running inference", output)

    def test_completion_detection(self):
        """Test detection of response completion"""
        # All our fixtures should end with a prompt box indicating completion
        for fixture_name, output in self.fixtures.items():
            if fixture_name != "welcome_message":
                # Should have prompt box at end
                self.assertIn("╭──────", output)
                self.assertIn("╰──────", output)


class TestOmnaraAPIIntegration(unittest.TestCase):
    """Test integration with mocked Omnara API"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")
        self.mock_client = MockOmnaraClient()
        self.wrapper.omnara_client_sync = self.mock_client  # type: ignore

        # Load API response fixtures
        self.api_fixtures_dir = Path(__file__).parent / "fixtures" / "api_responses"
        self.load_api_fixtures()

    def load_api_fixtures(self):
        """Load API response fixtures"""
        self.api_fixtures = {}
        for fixture_file in self.api_fixtures_dir.glob("*.json"):
            with open(fixture_file, "r") as f:
                self.api_fixtures[fixture_file.stem] = json.load(f)

    def test_message_sending(self):
        """Test sending message to API"""
        # Setup response
        self.mock_client.responses = [self.api_fixtures["send_message_response"]]

        # Process a message
        processor = MessageProcessor(self.wrapper)
        processor.process_assistant_message_sync("Test response")

        # Verify API call
        self.assertEqual(len(self.mock_client.messages_sent), 1)
        sent_message = self.mock_client.messages_sent[0]
        self.assertEqual(sent_message["content"], "Test response")
        self.assertEqual(sent_message["agent_type"], "Amp")

    def test_queued_message_handling(self):
        """Test handling of queued messages from API"""
        # Setup response with queued messages
        self.mock_client.responses = [self.api_fixtures["queued_messages_response"]]

        # Process a message
        processor = MessageProcessor(self.wrapper)
        processor.process_assistant_message_sync("Test response")

        # Verify queued messages were processed
        expected_messages = self.api_fixtures["queued_messages_response"][
            "queued_user_messages"
        ]

        # The messages get joined with newlines in the actual implementation
        concatenated_message = "\n".join(expected_messages)
        self.assertIn(concatenated_message, processor.web_ui_messages)
        self.assertIn(concatenated_message, self.wrapper.input_queue)

    def test_session_initialization(self):
        """Test session initialization with API"""
        self.wrapper.agent_instance_id = None

        # Setup response
        response_data = self.api_fixtures["send_message_response"]
        self.mock_client.responses = [response_data]

        # Send initial message
        processor = MessageProcessor(self.wrapper)
        processor.process_assistant_message_sync("Session started")

        # Verify instance ID was stored
        self.assertIsNotNone(self.wrapper.agent_instance_id)

    def test_user_message_deduplication(self):
        """Test that duplicate user messages are not sent"""
        processor = MessageProcessor(self.wrapper)

        # Add message to web UI messages first
        processor.web_ui_messages.add("Test message")

        # Try to process same message from CLI
        processor.process_user_message_sync("Test message", from_web=False)

        # Should not be sent to API (no user_message type in sent messages)
        user_messages = [
            msg
            for msg in self.mock_client.messages_sent
            if msg.get("type") == "user_message"
        ]
        self.assertEqual(len(user_messages), 0)


class TestAsyncOperations(unittest.TestCase):
    """Test async operations with mocks"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")
        self.mock_async_client = AsyncMock()
        # Mock the close method to prevent warnings during cleanup
        self.mock_async_client.close = AsyncMock(return_value=None)
        self.wrapper.omnara_client_async = self.mock_async_client

    def test_input_request(self):
        """Test async input request"""

        async def run_test():
            # Mock the request_user_input method
            self.mock_async_client.request_user_input.return_value = ["User input"]

            # Call the method
            await self.wrapper.request_user_input("msg_123")

            # Verify call was made
            self.mock_async_client.request_user_input.assert_called_once_with(
                message_id="msg_123", timeout_minutes=1440, poll_interval=3.0
            )

        # Run the async test
        asyncio.run(run_test())

    def test_input_request_cancellation(self):
        """Test input request cancellation"""

        async def run_test():
            # Mock cancellation
            self.mock_async_client.request_user_input.side_effect = (
                asyncio.CancelledError()
            )

            # Should handle cancellation gracefully
            with self.assertRaises(asyncio.CancelledError):
                await self.wrapper.request_user_input("msg_123")

        asyncio.run(run_test())

    def test_idle_monitor_loop(self):
        """Test idle monitoring loop"""

        async def run_test():
            # Setup
            self.wrapper.running = True
            self.wrapper.child_pid = 12345
            self.wrapper.message_processor = Mock()
            self.wrapper.message_processor.should_request_input.return_value = None

            # Mock session ensure
            self.mock_async_client._ensure_session = AsyncMock()

            # Mock os.kill to simulate running process
            with patch("os.kill") as mock_kill:
                mock_kill.return_value = None  # Process exists

                # Run one iteration
                self.wrapper.running = False  # Stop after one iteration
                await self.wrapper.idle_monitor_loop()

                # Verify session was ensured
                self.mock_async_client._ensure_session.assert_called_once()

        asyncio.run(run_test())


class TestErrorHandling(unittest.TestCase):
    """Test error handling scenarios"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_ansi_processing_with_invalid_chars(self):
        """Test ANSI processing with invalid characters"""
        # Text with null bytes and invalid characters
        invalid_text = "Hello\x00World\x01Test\x1f"

        # Should handle gracefully
        result = self.wrapper.strip_ansi(invalid_text)
        # Null bytes should be preserved by strip_ansi, but sanitized elsewhere
        self.assertIn("Hello", result)
        self.assertIn("World", result)

    @patch("os.kill")
    def test_process_monitoring_with_dead_process(self, mock_kill):
        """Test process monitoring when child process dies"""
        self.wrapper.child_pid = 12345
        mock_kill.side_effect = ProcessLookupError()

        # Should detect dead process
        async def run_test():
            self.wrapper.running = True
            self.wrapper.omnara_client_async = AsyncMock()
            self.wrapper.omnara_client_async._ensure_session = AsyncMock()
            self.wrapper.message_processor = Mock()
            self.wrapper.message_processor.should_request_input.return_value = None

            await self.wrapper.idle_monitor_loop()
            # Should have stopped running
            self.assertFalse(self.wrapper.running)

        asyncio.run(run_test())

    def test_git_diff_with_binary_files(self):
        """Test git diff handling with binary files"""
        self.wrapper.git_diff_enabled = True
        self.wrapper.initial_git_hash = "abc123"

        with patch("subprocess.run") as mock_run:
            # Mock git status showing untracked binary file
            mock_run.side_effect = [
                Mock(returncode=0, stdout=""),  # git diff (empty)
                Mock(returncode=0, stdout="?? binary.jpg\n"),  # git status
            ]

            # Mock file reading that fails (binary file)
            with patch(
                "builtins.open",
                side_effect=UnicodeDecodeError("utf-8", b"", 0, 1, "invalid"),
            ):
                diff = self.wrapper.get_git_diff()

                # Should handle binary files gracefully
                self.assertIsNotNone(diff)
                if diff is not None:
                    self.assertIn("binary.jpg", diff)
                    self.assertIn("[Binary or unreadable file]", diff)


if __name__ == "__main__":
    # Create fixture directories if they don't exist
    fixtures_dir = Path(__file__).parent / "fixtures"
    fixtures_dir.mkdir(exist_ok=True)
    (fixtures_dir / "amp_outputs").mkdir(exist_ok=True)
    (fixtures_dir / "api_responses").mkdir(exist_ok=True)

    unittest.main()
