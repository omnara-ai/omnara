"""Direct agent execution without relay (legacy behavior)."""

from __future__ import annotations

import os
import subprocess
import sys
from shutil import which
from typing import Iterable, List, Optional


AGENT_EXECUTABLE = {
    "claude": "claude",
    "amp": "amp",
}


def run_agent_direct(
    args,
    unknown_args: Optional[Iterable[str]],
    api_key: str,
) -> int:
    """
    Run agent CLI directly without relay wrapping.

    This provides the legacy behavior of directly executing the agent CLI
    without any relay streaming or PTY wrapping. Useful for troubleshooting
    or when relay is unavailable.

    Args:
        args: Parsed arguments from CLI
        unknown_args: Arguments to pass through to the underlying agent
        api_key: Omnara API key for authentication

    Returns:
        Exit code from the agent process
    """
    agent = getattr(args, "agent", "claude").lower()

    # Handle Codex separately
    if agent == "codex":
        from omnara.agents.codex import run_codex

        result = run_codex(args, unknown_args, api_key)
        return result if result is not None else 0

    # Build command
    executable = AGENT_EXECUTABLE.get(agent)
    if not executable:
        print(f"Error: Unknown agent '{agent}'", file=sys.stderr)
        return 1

    if which(executable) is None:
        print(
            f"Error: Could not locate '{executable}' on PATH. "
            f"Please install it or adjust PATH.",
            file=sys.stderr,
        )
        return 1

    command: List[str] = [executable]
    if unknown_args:
        command.extend(list(unknown_args))

    # Set up environment
    env = os.environ.copy()
    env.setdefault("OMNARA_API_KEY", api_key)

    base_url = getattr(args, "base_url", None)
    if base_url:
        env.setdefault("OMNARA_API_URL", base_url)

    agent_instance_id = getattr(args, "agent_instance_id", None)
    if agent_instance_id:
        env["OMNARA_AGENT_INSTANCE_ID"] = agent_instance_id

    # Execute directly
    try:
        result = subprocess.run(command, env=env)
        return result.returncode
    except KeyboardInterrupt:
        print("\nInterrupted by user", file=sys.stderr)
        return 130
    except Exception as e:
        print(f"Error executing {executable}: {e}", file=sys.stderr)
        return 1
