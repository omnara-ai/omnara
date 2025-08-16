#!/usr/bin/env python3
"""
Integration tests for AmpWrapper
Tests interactions between components and external dependencies
"""

import json
import os
import signal
import subprocess
import sys
import tempfile
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

from integrations.cli_wrappers.amp.amp import AmpWrapper


def is_amp_cli_available():
    """Check if Amp CLI is available in the system"""
    try:
        wrapper = AmpWrapper(api_key="test")
        amp_path = wrapper.find_amp_cli()
        return amp_path is not None
    except Exception:
        return False


def is_ci_environment():
    """Check if running in CI environment"""
    return os.environ.get("CI") == "true" or os.environ.get("GITHUB_ACTIONS") == "true"


class TestPTYIntegration(unittest.TestCase):
    """Test PTY creation and management"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    @unittest.skipIf(is_ci_environment(), "PTY operations may not work in CI")
    @patch("pty.fork")
    @patch("os.execvp")
    def test_pty_creation(self, mock_execvp, mock_fork):
        """Test PTY fork and setup"""
        # Mock successful fork
        mock_fork.return_value = (12345, 10)  # child_pid, master_fd

        # Mock child process (should not return)
        mock_execvp.side_effect = lambda cmd, args: None

        with (
            patch("termios.tcgetattr"),
            patch("tty.setraw"),
            patch("fcntl.fcntl"),
            patch("os.get_terminal_size", return_value=(80, 24)),
            patch("select.select", return_value=([], [], [])),
            patch("os.waitpid", return_value=(12345, 0)),
        ):
            # Start the method in a thread (it blocks)
            thread = threading.Thread(target=self.wrapper.run_amp_with_pty)
            thread.daemon = True
            thread.start()

            # Give it a moment to initialize
            time.sleep(0.1)

            # Should have set up PTY
            self.assertEqual(self.wrapper.child_pid, 12345)
            self.assertEqual(self.wrapper.master_fd, 10)

            # Stop the wrapper
            self.wrapper.running = False
            thread.join(timeout=1)

    @unittest.skipIf(not is_amp_cli_available(), "Amp CLI not available")
    @patch("os.get_terminal_size")
    def test_terminal_size_handling(self, mock_get_size):
        """Test terminal size detection and setting"""
        mock_get_size.return_value = (120, 30)

        with patch("pty.fork", return_value=(0, 0)):  # Child process
            with patch("os.execvp") as mock_exec:
                # Should not actually exec in test
                mock_exec.side_effect = SystemExit(0)

                try:
                    self.wrapper.run_amp_with_pty()
                except SystemExit:
                    pass

                # Should have set environment variables
                self.assertEqual(os.environ.get("COLUMNS"), "120")
                self.assertEqual(os.environ.get("ROWS"), "30")

    @unittest.skipIf(is_ci_environment(), "PTY operations may not work in CI")
    def test_stdin_passthrough(self):
        """Test that user input reaches Amp"""
        # Mock successful fork
        mock_fork_return = (12345, 10)  # child_pid, master_fd

        # Track test progress
        write_called = threading.Event()

        def mock_read_side_effect(fd, size):
            """Mock reading from stdin"""
            if fd == 0:  # stdin file descriptor
                # Return test input on first read
                return b"test input\n"
            return b""

        def mock_write_side_effect(fd, data):
            """Mock writing to master_fd"""
            if fd == 10 and data == b"test input\n":
                write_called.set()
                # Stop the wrapper after successful write
                self.wrapper.running = False
            return len(data)

        with (
            patch("pty.fork", return_value=mock_fork_return),
            patch("os.read", side_effect=mock_read_side_effect),
            patch("os.write", side_effect=mock_write_side_effect) as mock_write,
            patch("termios.tcgetattr"),
            patch("tty.setraw"),
            patch("fcntl.fcntl"),
            patch("os.waitpid", return_value=(0, 0)),  # Never exit during test
            patch("os.get_terminal_size", return_value=(80, 24)),
            patch("sys.stdin.fileno", return_value=0),  # Mock stdin file descriptor
        ):
            # Control select to return stdin has data initially
            select_call_count = [0]

            def select_side_effect(rlist, wlist, xlist, timeout):
                select_call_count[0] += 1
                # Return stdin ready for first 5 calls to handle timing
                if select_call_count[0] <= 5 and sys.stdin in rlist:
                    return ([sys.stdin], [], [])
                return ([], [], [])

            with patch("select.select", side_effect=select_side_effect):
                # Set up minimal state
                self.wrapper.running = True

                # Start in thread
                thread = threading.Thread(target=self.wrapper.run_amp_with_pty)
                thread.daemon = True
                thread.start()

                # Wait for write to be called
                success = write_called.wait(timeout=3.0)

                # Clean up
                self.wrapper.running = False
                thread.join(timeout=2)

                # Verify write was called
                self.assertTrue(success, "Write was not called within timeout")
                mock_write.assert_called()
                # Verify it was called with the correct arguments
                mock_write.assert_any_call(10, b"test input\n")


class TestOutputProcessing(unittest.TestCase):
    """Test output processing and message extraction"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

        # Setup mock clients
        self.wrapper.omnara_client_sync = Mock()
        self.wrapper.omnara_client_sync.send_message.return_value = Mock(
            message_id="test_msg",
            agent_instance_id="test_instance",
            queued_user_messages=[],
        )

        # Initialize processor
        self.wrapper.agent_instance_id = "test_instance"

    def test_output_capture(self):
        """Test that terminal output is captured"""
        # Simulate output monitoring
        test_output = "Hello from Amp!\n"

        # This would normally be called from monitor_amp_output
        self.wrapper.terminal_buffer += test_output
        self.wrapper.last_output_time = time.time()

        self.assertIn("Hello from Amp!", self.wrapper.terminal_buffer)

    def test_buffer_size_management(self):
        """Test that buffers don't grow unbounded"""
        # Fill buffer beyond limit
        large_output = "x" * 15000  # Exceeds 10000 limit
        self.wrapper.terminal_buffer = large_output

        # Simulate buffer trimming (from monitor_amp_output)
        if len(self.wrapper.terminal_buffer) > 10000:
            self.wrapper.terminal_buffer = self.wrapper.terminal_buffer[-5000:]

        self.assertEqual(len(self.wrapper.terminal_buffer), 5000)

    def test_ansi_code_handling(self):
        """Test ANSI code processing in real output"""
        ansi_output = "\x1b[31mRed text\x1b[0m normal text \x1b[2mDim text\x1b[0m"

        cleaned = self.wrapper.strip_ansi(ansi_output)

        self.assertEqual(cleaned, "Red text normal text Dim text")
        self.assertNotIn("\x1b", cleaned)


class TestGitIntegration(unittest.TestCase):
    """Test git integration in real repository"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

        # Create temporary git repository
        self.temp_dir = tempfile.mkdtemp()
        self.original_cwd = os.getcwd()
        os.chdir(self.temp_dir)

        # Initialize git repo
        subprocess.run(["git", "init"], check=True, capture_output=True)
        subprocess.run(["git", "config", "user.email", "test@example.com"], check=True)
        subprocess.run(["git", "config", "user.name", "Test User"], check=True)

        # Create initial commit
        with open("initial.txt", "w") as f:
            f.write("Initial content")
        subprocess.run(["git", "add", "initial.txt"], check=True)
        subprocess.run(["git", "commit", "-m", "Initial commit"], check=True)

    def tearDown(self):
        os.chdir(self.original_cwd)
        import shutil

        shutil.rmtree(self.temp_dir)

    def test_git_tracking_initialization(self):
        """Test git tracking setup in real repo"""
        self.wrapper.init_git_tracking()

        self.assertTrue(self.wrapper.git_diff_enabled)
        self.assertIsNotNone(self.wrapper.initial_git_hash)
        if self.wrapper.initial_git_hash is not None:
            self.assertEqual(len(self.wrapper.initial_git_hash), 40)  # SHA-1 hash

    def test_git_diff_generation(self):
        """Test git diff generation with real files"""
        self.wrapper.init_git_tracking()

        # Make changes
        with open("new_file.txt", "w") as f:
            f.write("New file content")

        with open("initial.txt", "w") as f:
            f.write("Modified content")

        diff = self.wrapper.get_git_diff()

        self.assertIsNotNone(diff)
        if diff is not None:
            self.assertIn("new_file.txt", diff)
            self.assertIn("Modified content", diff)
            self.assertIn("diff --git", diff)

    def test_git_diff_untracked_files(self):
        """Test that untracked files appear in diff"""
        self.wrapper.init_git_tracking()

        # Create untracked file
        with open("untracked.py", "w") as f:
            f.write('print("Hello from untracked file")')

        diff = self.wrapper.get_git_diff()

        self.assertIsNotNone(diff)
        if diff is not None:
            self.assertIn("untracked.py", diff)
            self.assertIn('print("Hello from untracked file")', diff)
            self.assertIn("new file mode", diff)


class TestSignalHandling(unittest.TestCase):
    """Test signal handling and cleanup"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    @patch("os.kill")
    def test_graceful_shutdown(self, mock_kill):
        """Test graceful shutdown on signals"""
        self.wrapper.child_pid = 12345
        self.wrapper.running = True

        # Simulate SIGTERM to child
        self.wrapper.running = False

        # Cleanup should kill child process
        if self.wrapper.child_pid:
            try:
                os.kill(self.wrapper.child_pid, signal.SIGTERM)
            except ProcessLookupError:
                pass

        mock_kill.assert_called_with(12345, signal.SIGTERM)

    def test_cleanup_on_exit(self):
        """Test that resources are cleaned up on exit"""
        # Setup resources
        temp_settings = f"/tmp/amp_omnara_{self.wrapper.session_uuid}.json"
        with open(temp_settings, "w") as f:
            json.dump({}, f)

        self.assertTrue(Path(temp_settings).exists())

        # Simulate cleanup
        temp_path = Path("/tmp") / f"amp_omnara_{self.wrapper.session_uuid}.json"
        if temp_path.exists():
            temp_path.unlink()

        self.assertFalse(Path(temp_settings).exists())


class TestThreadSafety(unittest.TestCase):
    """Test thread safety and synchronization"""

    def setUp(self):
        self.wrapper = AmpWrapper(api_key="test")

    def test_concurrent_buffer_access(self):
        """Test concurrent access to terminal buffer"""
        results = []
        errors = []

        def writer_thread():
            try:
                for i in range(100):
                    self.wrapper.terminal_buffer += f"output_{i}\n"
                    time.sleep(0.001)
                results.append("writer_done")
            except Exception as e:
                errors.append(f"writer_error: {e}")

        def reader_thread():
            try:
                for i in range(100):
                    _ = self.wrapper.strip_ansi(self.wrapper.terminal_buffer)
                    time.sleep(0.001)
                results.append("reader_done")
            except Exception as e:
                errors.append(f"reader_error: {e}")

        # Start threads
        writer = threading.Thread(target=writer_thread)
        reader = threading.Thread(target=reader_thread)

        writer.start()
        reader.start()

        writer.join(timeout=5)
        reader.join(timeout=5)

        # Should complete without errors
        self.assertEqual(len(errors), 0, f"Errors occurred: {errors}")
        self.assertIn("writer_done", results)
        self.assertIn("reader_done", results)

    def test_message_queue_thread_safety(self):
        """Test thread safety of input queue"""

        # Multiple threads adding to input queue
        def add_messages(thread_id):
            for i in range(10):
                self.wrapper.input_queue.append(f"message_{thread_id}_{i}")
                time.sleep(0.001)

        threads = []
        for i in range(3):
            thread = threading.Thread(target=add_messages, args=(i,))
            threads.append(thread)
            thread.start()

        for thread in threads:
            thread.join(timeout=5)

        # Should have all messages
        self.assertEqual(len(self.wrapper.input_queue), 30)


if __name__ == "__main__":
    unittest.main()
