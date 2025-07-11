#!/usr/bin/env python3
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
    # Since we're running from an installed package, we need to recreate
    # the stdio server's argument parsing
    import sys

    # Build the command to run the stdio server module directly
    cmd = [
        sys.executable,
        "-m",
        "servers.mcp_server.stdio_server",
        "--api-key",
        args.api_key,
    ]
    if args.base_url:
        cmd.extend(["--base-url", args.base_url])

    # Run the stdio server in the same process
    subprocess.run(cmd)


def run_webhook_server(cloudflare_tunnel=False):
    """Run the Claude Code webhook FastAPI server"""
    cloudflared_process = None

    if cloudflare_tunnel:
        # Check if cloudflared is available and start tunnel
        try:
            # Test if cloudflared is installed
            test_cmd = ["cloudflared", "--version"]
            subprocess.run(test_cmd, capture_output=True, check=True)

            # Start cloudflare tunnel
            print("[INFO] Starting Cloudflare tunnel...")
            cloudflared_cmd = [
                "cloudflared",
                "tunnel",
                "--url",
                "http://localhost:6662",
            ]
            cloudflared_process = subprocess.Popen(cloudflared_cmd)

            # Give cloudflared a moment to start
            import time

            time.sleep(3)

            # Check if process is still running
            if cloudflared_process.poll() is not None:
                print("\n[ERROR] Cloudflare tunnel failed to start")
                sys.exit(1)

        except (subprocess.CalledProcessError, FileNotFoundError):
            print("\n[ERROR] cloudflared is not installed!")
            print("Please install cloudflared to use the --cloudflare-tunnel option.")
            print(
                "Visit: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/"
            )
            print("for installation instructions.")
            sys.exit(1)

    # Use uvicorn to run the webhook app module
    cmd = [
        sys.executable,
        "-m",
        "uvicorn",
        "webhooks.claude_code:app",
        "--host",
        "0.0.0.0",
        "--port",
        "6662",
    ]

    print("[INFO] Starting Claude Code webhook server on port 6662")

    try:
        # If uvicorn is not installed, subprocess will fail with a clear error
        subprocess.run(cmd)
    finally:
        # Clean up cloudflared process if it exists
        if cloudflared_process:
            cloudflared_process.terminate()
            cloudflared_process.wait()


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

  # Run with custom API base URL
  omnara --stdio --api-key YOUR_API_KEY --base-url http://localhost:8000
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

    # Arguments for stdio mode
    parser.add_argument(
        "--api-key", help="API key for authentication (required for stdio mode)"
    )
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Base URL of the Omnara API server (stdio mode only)",
    )

    args = parser.parse_args()

    # Validate cloudflare-tunnel is only used with webhook mode
    if args.cloudflare_tunnel and not args.claude_code_webhook:
        parser.error("--cloudflare-tunnel can only be used with --claude-code-webhook")

    # Determine which mode to run
    if args.claude_code_webhook:
        # Run webhook server
        run_webhook_server(cloudflare_tunnel=args.cloudflare_tunnel)
    else:
        # Default to stdio server
        if not args.api_key:
            parser.error("--api-key is required for stdio mode")
        run_stdio_server(args)


if __name__ == "__main__":
    main()
