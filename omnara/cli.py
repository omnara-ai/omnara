"""Omnara Main Entry Point

This is the main entry point for the omnara command that dispatches to either:
- MCP stdio server (default or with --stdio)
- Claude Code webhook server (with --claude-code-webhook)
"""

import argparse
import sys
import subprocess


def run_stdio_server(args):
    """Run the MCP stdio server with the provided arguments"""
    cmd = [
        sys.executable,
        "-m",
        "servers.mcp_server.stdio_server",
        "--api-key",
        args.api_key,
    ]
    if args.base_url:
        cmd.extend(["--base-url", args.base_url])
    if (
        hasattr(args, "claude_code_permission_tool")
        and args.claude_code_permission_tool
    ):
        cmd.append("--claude-code-permission-tool")
    if hasattr(args, "git_diff") and args.git_diff:
        cmd.append("--git-diff")

    subprocess.run(cmd)


def run_webhook_server(
    cloudflare_tunnel=False, dangerously_skip_permissions=False, port=None
):
    """Run the Claude Code webhook FastAPI server"""
    cmd = [
        sys.executable,
        "-m",
        "webhooks.claude_code",
    ]

    if dangerously_skip_permissions:
        cmd.append("--dangerously-skip-permissions")

    if cloudflare_tunnel:
        cmd.append("--cloudflare-tunnel")

    if port is not None:
        cmd.extend(["--port", str(port)])

    print("[INFO] Starting Claude Code webhook server...")
    subprocess.run(cmd)


def main():
    """Main entry point that dispatches based on command line arguments"""
    parser = argparse.ArgumentParser(
        description="Omnara - AI Agent Dashboard and Tools",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run MCP stdio server (default)
  omnara --api-key YOUR_API_KEY

  # Run MCP stdio server explicitly
  omnara --stdio --api-key YOUR_API_KEY

  # Run Claude Code webhook server
  omnara --claude-code-webhook

  # Run webhook server with Cloudflare tunnel
  omnara --claude-code-webhook --cloudflare-tunnel

  # Run webhook server on custom port
  omnara --claude-code-webhook --port 8080

  # Run with custom API base URL
  omnara --stdio --api-key YOUR_API_KEY --base-url http://localhost:8000

  # Run with git diff capture enabled
  omnara --api-key YOUR_API_KEY --git-diff
        """,
    )

    # Add mutually exclusive group for server modes
    mode_group = parser.add_mutually_exclusive_group()
    mode_group.add_argument(
        "--stdio",
        action="store_true",
        help="Run the MCP stdio server (default if no mode specified)",
    )
    mode_group.add_argument(
        "--claude-code-webhook",
        action="store_true",
        help="Run the Claude Code webhook server",
    )

    # Arguments for webhook mode
    parser.add_argument(
        "--cloudflare-tunnel",
        action="store_true",
        help="Run Cloudflare tunnel for the webhook server (webhook mode only)",
    )
    parser.add_argument(
        "--dangerously-skip-permissions",
        action="store_true",
        help="Skip permission prompts in Claude Code (webhook mode only) - USE WITH CAUTION",
    )
    parser.add_argument(
        "--port",
        type=int,
        help="Port to run the webhook server on (webhook mode only, default: 6662)",
    )

    # Arguments for stdio mode
    parser.add_argument(
        "--api-key", help="API key for authentication (required for stdio mode)"
    )
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Base URL of the Omnara API server (stdio mode only)",
    )
    parser.add_argument(
        "--claude-code-permission-tool",
        action="store_true",
        help="Enable Claude Code permission prompt tool for handling tool execution approvals (stdio mode only)",
    )
    parser.add_argument(
        "--git-diff",
        action="store_true",
        help="Enable git diff capture for log_step and ask_question (stdio mode only)",
    )

    args = parser.parse_args()

    if args.cloudflare_tunnel and not args.claude_code_webhook:
        parser.error("--cloudflare-tunnel can only be used with --claude-code-webhook")

    if args.port is not None and not args.claude_code_webhook:
        parser.error("--port can only be used with --claude-code-webhook")

    if args.claude_code_webhook:
        run_webhook_server(
            cloudflare_tunnel=args.cloudflare_tunnel,
            dangerously_skip_permissions=args.dangerously_skip_permissions,
            port=args.port,
        )
    else:
        if not args.api_key:
            parser.error("--api-key is required for stdio mode")
        run_stdio_server(args)


if __name__ == "__main__":
    main()
