"""Omnara Main Entry Point

This is the main entry point for the omnara command that dispatches to either:
- MCP stdio server (default or with --stdio)
- Claude Code webhook server (with --claude-code-webhook)
"""

import argparse
import sys
import subprocess
import time


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

    subprocess.run(cmd)


def run_webhook_server(cloudflare_tunnel=False):
    """Run the Claude Code webhook FastAPI server"""
    cloudflared_process = None

    if cloudflare_tunnel:
        try:
            test_cmd = ["cloudflared", "--version"]
            subprocess.run(test_cmd, capture_output=True, check=True)

            print("[INFO] Starting Cloudflare tunnel...")
            cloudflared_cmd = [
                "cloudflared",
                "tunnel",
                "--url",
                "http://localhost:6662",
            ]
            cloudflared_process = subprocess.Popen(cloudflared_cmd)

            # Give cloudflared a moment to start
            time.sleep(3)

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
        subprocess.run(cmd)
    finally:
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

    if args.cloudflare_tunnel and not args.claude_code_webhook:
        parser.error("--cloudflare-tunnel can only be used with --claude-code-webhook")

    if args.claude_code_webhook:
        run_webhook_server(cloudflare_tunnel=args.cloudflare_tunnel)
    else:
        if not args.api_key:
            parser.error("--api-key is required for stdio mode")
        run_stdio_server(args)


if __name__ == "__main__":
    main()
