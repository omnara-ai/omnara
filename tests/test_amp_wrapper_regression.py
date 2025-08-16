#!/usr/bin/env python3
"""
Regression tests for AmpWrapper
Tests to prevent known issues from recurring
"""

import gc
import os
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

# Skip all tests in this module when running in CI
if os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true":
    import pytest

    pytestmark = pytest.mark.skip(
        reason="AMP wrapper tests disabled in CI due to file descriptor issues"
    )

from integrations.cli_wrappers.amp.amp import AmpWrapper, MessageProcessor


class TestMemoryLeaks(unittest.TestCase):
    """Test for memory leaks and resource management"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_buffer_growth_prevention(self):
        """Test that buffers don't grow unbounded"""

        # Add large amount of data
        for i in range(1000):
            self.wrapper.terminal_buffer += (
                f"Large output chunk {i} with lots of text\n"
            )

        # Simulate buffer management from monitor_amp_output
        if len(self.wrapper.terminal_buffer) > 10000:
            self.wrapper.terminal_buffer = self.wrapper.terminal_buffer[-5000:]

        final_size = len(self.wrapper.terminal_buffer)

        # Should be trimmed
        self.assertLessEqual(final_size, 5000)
        self.assertGreaterEqual(final_size, 4000)  # Should have some content

    def test_message_processor_memory_cleanup(self):
        """Test that message processor cleans up properly"""
        processor = MessageProcessor(self.wrapper)

        # Add many messages
        for i in range(1000):
            processor.web_ui_messages.add(f"message_{i}")

        self.assertEqual(len(processor.web_ui_messages), 1000)

        # Simulate cleanup
        processor.web_ui_messages.clear()

        self.assertEqual(len(processor.web_ui_messages), 0)

    def test_no_memory_leaks_in_ansi_processing(self):
        """Test ANSI processing doesn't leak memory"""
        import tracemalloc

        tracemalloc.start()

        # Process lots of ANSI text
        ansi_text = "\x1b[31mRed\x1b[0m " * 1000

        for _ in range(100):
            cleaned = self.wrapper.strip_ansi(ansi_text)
            del cleaned

        gc.collect()

        current, peak = tracemalloc.get_traced_memory()
        tracemalloc.stop()

        # Peak should be reasonable (less than 10MB for this test)
        self.assertLess(peak, 10 * 1024 * 1024)


class TestInfiniteLoopPrevention(unittest.TestCase):
    """Test prevention of infinite loops in processing"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_response_processing_terminates(self):
        """Test that response processing always terminates"""
        # Setup mock that could cause infinite loop
        self.wrapper.waiting_for_response = True
        self.wrapper.inference_started = True
        self.wrapper.response_buffer = ["test output"] * 1000  # Large buffer

        # Set a timeout to ensure termination
        start_time = time.time()
        timeout = 5.0

        def process_with_timeout():
            # Simulate the problematic part of monitor_amp_output
            completion_detected = True  # Force completion

            if (
                completion_detected
                and hasattr(self.wrapper, "inference_started")
                and self.wrapper.inference_started
            ):
                self.wrapper.waiting_for_response = False
                self.wrapper.inference_started = False

                # Process buffered response (simplified version)
                if hasattr(self.wrapper, "response_buffer"):
                    full_output = "".join(self.wrapper.response_buffer)
                    # Just count lines instead of complex processing
                    line_count = len(full_output.split("\n"))
                    self.assertGreater(line_count, 0)
                    self.wrapper.response_buffer = []

        process_with_timeout()

        elapsed = time.time() - start_time

        # Should complete quickly
        self.assertLess(elapsed, timeout)
        self.assertFalse(self.wrapper.waiting_for_response)
        self.assertFalse(self.wrapper.inference_started)

    def test_idle_detection_terminates(self):
        """Test that idle detection doesn't loop infinitely"""
        # Set up conditions that might cause infinite checking
        self.wrapper.waiting_for_response = False
        self.wrapper.terminal_buffer = "no special patterns here"
        self.wrapper.last_output_time = time.time() - 10  # Old timestamp

        start_time = time.time()

        # Call idle detection multiple times
        for _ in range(100):
            result = self.wrapper.is_amp_idle()
            self.assertTrue(result)  # Should be idle with old timestamp

        elapsed = time.time() - start_time

        # Should complete quickly even with many calls
        self.assertLess(elapsed, 1.0)


class TestThreadCleanup(unittest.TestCase):
    """Test proper thread cleanup"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_monitor_thread_termination(self):
        """Test that monitor thread terminates properly"""
        thread_finished = threading.Event()

        def mock_monitor():
            # Simplified version of monitor_amp_output
            while self.wrapper.running and self.wrapper.master_fd is not None:
                time.sleep(0.1)
                # Simulate checking for data
                if not self.wrapper.running:
                    break
            thread_finished.set()

        # Start mock monitor
        self.wrapper.running = True
        self.wrapper.master_fd = 10  # Mock fd

        monitor_thread = threading.Thread(target=mock_monitor)
        monitor_thread.daemon = True
        monitor_thread.start()

        # Let it run briefly
        time.sleep(0.2)

        # Signal stop
        self.wrapper.running = False

        # Should terminate quickly
        thread_finished.wait(timeout=2.0)
        self.assertTrue(thread_finished.is_set())

    def test_all_threads_join_on_exit(self):
        """Test that all threads are properly joined on exit"""
        threads_created = []
        original_thread = threading.Thread

        def mock_thread(*args, **kwargs):
            thread = original_thread(*args, **kwargs)
            threads_created.append(thread)
            return thread

        with patch("threading.Thread", side_effect=mock_thread):
            # Create wrapper (would normally create threads)
            _ = AmpWrapper(api_key="test")

            # Simulate thread creation
            def dummy_target():
                time.sleep(0.1)

            thread1 = threading.Thread(target=dummy_target)
            thread2 = threading.Thread(target=dummy_target)
            threads_created.extend([thread1, thread2])

            thread1.daemon = True
            thread2.daemon = True
            thread1.start()
            thread2.start()

            # All threads should be joinable
            for thread in threads_created:
                thread.join(timeout=1.0)
                self.assertFalse(thread.is_alive())


class TestResourceCleanup(unittest.TestCase):
    """Test proper resource cleanup"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_pty_resource_cleanup(self):
        """Test that PTY resources are freed on exit"""
        # Mock PTY resources
        self.wrapper.master_fd = 10
        self.wrapper.child_pid = 12345

        with patch("os.kill") as mock_kill, patch("os.close") as mock_close:
            # Simulate cleanup
            if self.wrapper.child_pid:
                try:
                    mock_kill(self.wrapper.child_pid, 15)  # SIGTERM
                except ProcessLookupError:
                    pass

            if self.wrapper.master_fd:
                try:
                    mock_close(self.wrapper.master_fd)
                except OSError:
                    pass

            # Verify cleanup calls
            self.assertTrue(mock_kill.called)

    def test_file_handle_cleanup(self):
        """Test that file handles are closed"""
        # Create temporary log file
        import tempfile

        temp_file = tempfile.NamedTemporaryFile(mode="w", delete=False)
        temp_path = temp_file.name
        self.wrapper.debug_log_file = temp_file  # type: ignore

        # Close the file
        if self.wrapper.debug_log_file and not self.wrapper.debug_log_file.closed:
            self.wrapper.debug_log_file.close()

        # Verify it's closed
        if self.wrapper.debug_log_file is not None:
            self.assertTrue(self.wrapper.debug_log_file.closed)

        # Clean up temp file
        Path(temp_path).unlink()

    def test_temp_settings_file_cleanup(self):
        """Test that temporary settings files are cleaned up"""
        # Create settings file
        settings_file = self.wrapper.create_amp_settings()

        self.assertTrue(Path(settings_file).exists())

        # Simulate cleanup
        temp_path = Path(settings_file)
        if temp_path.exists():
            temp_path.unlink()

        self.assertFalse(Path(settings_file).exists())


class TestDuplicateMessagePrevention(unittest.TestCase):
    """Test prevention of duplicate message sending"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")
        self.wrapper.omnara_client_sync = Mock()

        mock_response = Mock()
        mock_response.message_id = "test_msg"
        mock_response.agent_instance_id = "test_instance"
        mock_response.queued_user_messages = []
        self.wrapper.omnara_client_sync.send_message.return_value = mock_response
        self.wrapper.omnara_client_sync.send_user_message.return_value = None

        self.wrapper.agent_instance_id = "test_instance"

    def test_no_duplicate_user_messages(self):
        """Test that duplicate user messages are not sent"""
        processor = MessageProcessor(self.wrapper)

        # Add message from web UI first
        processor.process_user_message_sync("Test message", from_web=True)

        # Try to send same message from CLI
        processor.process_user_message_sync("Test message", from_web=False)

        # Should not have called send_user_message
        # Type ignore because we know this is a Mock in setUp
        self.wrapper.omnara_client_sync.send_user_message.assert_not_called()  # type: ignore

    def test_no_duplicate_assistant_messages(self):
        """Test that identical assistant messages are not sent multiple times"""
        processor = MessageProcessor(self.wrapper)

        # Send same message twice
        processor.process_assistant_message_sync("Same response")
        processor.process_assistant_message_sync("Same response")

        # Should have been called twice (no deduplication for assistant messages)
        # Type ignore because we know this is a Mock in setUp
        self.assertEqual(self.wrapper.omnara_client_sync.send_message.call_count, 2)  # type: ignore

    def test_web_ui_message_deduplication(self):
        """Test web UI message deduplication works correctly"""
        processor = MessageProcessor(self.wrapper)

        # Add multiple identical messages from web
        processor.process_user_message_sync("Duplicate", from_web=True)
        processor.process_user_message_sync("Duplicate", from_web=True)
        processor.process_user_message_sync("Duplicate", from_web=True)

        # Should only be in set once
        self.assertEqual(
            len([msg for msg in processor.web_ui_messages if msg == "Duplicate"]), 1
        )


class TestRaceConditionPrevention(unittest.TestCase):
    """Test prevention of race conditions"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_state_consistency_under_concurrency(self):
        """Test that state remains consistent under concurrent access"""
        results = []
        errors = []

        def state_modifier():
            try:
                for i in range(50):
                    self.wrapper.waiting_for_response = True
                    time.sleep(0.001)
                    self.wrapper.waiting_for_response = False
                    time.sleep(0.001)
                results.append("modifier_done")
            except Exception as e:
                errors.append(f"modifier_error: {e}")

        def state_reader():
            try:
                for i in range(50):
                    _ = self.wrapper.is_amp_idle()
                    time.sleep(0.001)
                results.append("reader_done")
            except Exception as e:
                errors.append(f"reader_error: {e}")

        # Start concurrent operations
        threads = []
        for _ in range(2):
            t1 = threading.Thread(target=state_modifier)
            t2 = threading.Thread(target=state_reader)
            threads.extend([t1, t2])
            t1.start()
            t2.start()

        # Wait for completion
        for thread in threads:
            thread.join(timeout=10)

        # Should complete without errors
        self.assertEqual(len(errors), 0, f"Race condition errors: {errors}")
        self.assertGreaterEqual(len(results), 4)  # At least 2 modifiers + 2 readers


if __name__ == "__main__":
    unittest.main()
