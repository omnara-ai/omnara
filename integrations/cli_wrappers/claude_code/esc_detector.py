"""
ESC Detector Process - Detects "esc to interrupt" indicators in terminal output.

This runs in a separate process to bypass the GIL and ensure responsive detection
even under high system load.
"""

import multiprocessing as mp
import time
from typing import Optional
import signal


class EscDetector:
    """Detects ESC indicators in terminal output using a separate process."""

    def __init__(self, agent_instance_id: Optional[str] = None):
        """Initialize the detector with shared memory and process management.

        Args:
            agent_instance_id: Optional ID for creating log file
        """
        # Shared memory for timestamp (double precision float)
        self._last_esc_seen = mp.Value("d", 0.0)

        # Communication pipe
        self._parent_conn, self._child_conn = mp.Pipe()

        # Process management
        self._process: Optional[mp.Process] = None
        self._running = mp.Value("b", False)  # shared boolean
        self._agent_instance_id = agent_instance_id

    def start(self) -> None:
        """Start the detector process."""
        if self._process and self._process.is_alive():
            return

        self._running.value = True
        self._process = mp.Process(
            target=self._detector_worker,
            args=(
                self._child_conn,
                self._last_esc_seen,
                self._running,
                self._agent_instance_id,
            ),
            daemon=True,
        )
        self._process.start()

    def stop(self) -> None:
        """Stop the detector process gracefully."""
        if not self._process:
            return

        # Signal shutdown
        self._running.value = False

        # Send sentinel to wake up the worker
        try:
            self._parent_conn.send(b"")
        except Exception:
            pass

        # Wait for clean shutdown (max 1 second)
        self._process.join(timeout=1.0)

        # Force terminate if still running
        if self._process.is_alive():
            self._process.terminate()
            self._process.join(timeout=0.5)

        self._process = None

    def feed(self, data: bytes) -> None:
        """
        Feed terminal output data to the detector.

        Args:
            data: Raw bytes from terminal output
        """
        if not self._process or not self._process.is_alive():
            return

        try:
            # Non-blocking send
            if self._parent_conn.poll(timeout=0):
                # Clear any backlog first
                try:
                    self._parent_conn.recv()
                except Exception:
                    pass

            self._parent_conn.send(data)
        except Exception:
            # Ignore send errors (pipe might be full or closed)
            pass

    @property
    def last_esc_seen(self) -> Optional[float]:
        """
        Get the timestamp of the last ESC detection.

        Returns:
            Unix timestamp or None if never detected
        """
        timestamp = self._last_esc_seen.value
        return timestamp if timestamp > 0 else None

    def is_alive(self) -> bool:
        """Check if the detector process is running."""
        return bool(self._process and self._process.is_alive())

    @staticmethod
    def _detector_worker(conn, shared_timestamp, running, agent_instance_id):
        """
        Worker process that detects ESC indicators.

        This runs in a separate process to avoid GIL contention.
        """
        # Ignore signals in worker process
        signal.signal(signal.SIGINT, signal.SIG_IGN)

        # Set up logging to separate file
        log_file = None
        if agent_instance_id:
            try:
                from pathlib import Path

                log_dir = Path.home() / ".omnara" / "claude_wrapper"
                log_dir.mkdir(exist_ok=True, parents=True)
                log_path = log_dir / f"{agent_instance_id}_esc.log"
                log_file = open(log_path, "w")

                def log(msg):
                    timestamp = time.strftime("%H:%M:%S", time.localtime())
                    ms = int((time.time() % 1) * 1000)
                    log_file.write(f"[{timestamp}.{ms:03d}] {msg}\n")
                    log_file.flush()
            except Exception:

                def log(msg):
                    pass
        else:

            def log(msg):
                pass

        # Small buffer for edge cases where indicator spans chunks
        buffer = b""
        MAX_BUFFER_SIZE = 1000

        # Indicators to detect (as bytes for speed)
        INDICATORS = [b"esc to interrupt)", b"ctrl+b to run in background"]

        chunk_count = 0

        while running.value:
            try:
                # Check for new data (timeout allows periodic shutdown check)
                if conn.poll(timeout=0.05):
                    data = conn.recv()
                    chunk_count += 1

                    # Empty data is shutdown signal
                    if not data:
                        break

                    # Log what we received - SHOW RAW DATA
                    log(f"Chunk #{chunk_count}: {len(data)} bytes: {repr(data)}")

                    # Check if ESC is in this chunk
                    if b"esc to interrupt)" in data:
                        log(f"*** ESC FOUND IN CHUNK #{chunk_count} ***")

                    # Add to buffer
                    buffer += data

                    # Keep buffer bounded
                    if len(buffer) > MAX_BUFFER_SIZE:
                        buffer = buffer[-MAX_BUFFER_SIZE:]

                    # Check for indicators
                    for indicator in INDICATORS:
                        if indicator in buffer:
                            shared_timestamp.value = time.time()
                            log(
                                f"DETECTED '{indicator.decode()}' at {shared_timestamp.value:.3f}"
                            )
                            # Clear buffer after detection to avoid duplicates
                            buffer = b""
                            break

            except (EOFError, OSError):
                # Pipe closed, exit gracefully
                break
            except Exception:
                # Ignore other errors and continue
                pass

    def __enter__(self):
        """Context manager entry."""
        self.start()
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        """Context manager exit."""
        self.stop()

    def __del__(self):
        """Cleanup on deletion."""
        try:
            self.stop()
        except Exception:
            pass
