"""Run agent with WebSocket terminal relay streaming."""

from __future__ import annotations

import os
from typing import Iterable, Optional

from omnara.session_sharing import run_agent_with_relay


def run_agent_with_terminal_relay(
    args,
    unknown_args: Optional[Iterable[str]],
    api_key: str,
) -> int:
    """
    Launch agent CLI with WebSocket terminal relay.

    This is the main entry point for running agents (Claude, Amp, Codex, etc.) with
    terminal streaming to the Omnara dashboard via WebSocket relay.

    All agents are executed by calling their locally installed executable
    (e.g., 'claude', 'amp', 'codex') with relay wrapping.

    Args:
        args: Parsed arguments from CLI
        unknown_args: Arguments to pass through to the underlying agent
        api_key: Omnara API key for authentication

    Returns:
        Exit code from the agent process
    """
    agent = getattr(args, "agent", "claude").lower()

    # Configure relay settings from args
    if getattr(args, "no_relay", False):
        os.environ["OMNARA_RELAY_DISABLED"] = "1"

    relay_url = getattr(args, "relay_url", None)
    if relay_url:
        os.environ["OMNARA_RELAY_URL"] = relay_url

    # Run the agent with relay
    exit_code = run_agent_with_relay(agent, args, unknown_args, api_key)
    return exit_code
