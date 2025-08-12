"""Omnara Main Entry Point

This is the main entry point for the omnara command that supports:
- Default (no subcommand): Claude chat integration
- serve: Webhook server with tunnel options
- mcp: MCP stdio server
"""

import argparse
import sys
import subprocess
import json
import os
from pathlib import Path
import webbrowser
import urllib.parse
from http.server import HTTPServer, BaseHTTPRequestHandler
import secrets
import requests
import time
import threading


def get_current_version():
    """Get the current installed version of omnara"""
    try:
        from omnara import __version__

        return __version__
    except Exception:
        return "unknown"


def check_for_updates():
    """Check PyPI for a newer version of omnara"""
    try:
        response = requests.get("https://pypi.org/pypi/omnara/json", timeout=2)
        latest_version = response.json()["info"]["version"]
        current_version = get_current_version()

        if latest_version != current_version and current_version != "unknown":
            print(f"\n✨ Update available: {current_version} → {latest_version}")
            print("   Run: pip install --upgrade omnara\n")
    except Exception:
        pass


def get_credentials_path():
    """Get the path to the credentials file"""
    config_dir = Path.home() / ".omnara"
    return config_dir / "credentials.json"


def load_stored_api_key():
    """Load API key from credentials file if it exists"""
    credentials_path = get_credentials_path()

    if not credentials_path.exists():
        return None

    try:
        with open(credentials_path, "r") as f:
            data = json.load(f)
            api_key = data.get("write_key")
            if api_key and isinstance(api_key, str):
                return api_key
            else:
                print("Warning: Invalid API key format in credentials file.")
                return None
    except json.JSONDecodeError:
        print(
            "Warning: Corrupted credentials file. Please re-authenticate with --reauth."
        )
        return None
    except (KeyError, IOError) as e:
        print(f"Warning: Error reading credentials file: {str(e)}")
        return None


def save_api_key(api_key):
    """Save API key to credentials file"""
    credentials_path = get_credentials_path()

    # Create directory if it doesn't exist
    credentials_path.parent.mkdir(mode=0o700, exist_ok=True)

    # Save the API key
    data = {"write_key": api_key}
    with open(credentials_path, "w") as f:
        json.dump(data, f, indent=2)

    # Set file permissions to 600 (read/write for owner only)
    os.chmod(credentials_path, 0o600)


class AuthHTTPServer(HTTPServer):
    """Custom HTTP server with attributes for authentication"""

    api_key: str | None
    state: str | None

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.api_key = None
        self.state = None


class AuthCallbackHandler(BaseHTTPRequestHandler):
    """HTTP handler for the OAuth callback"""

    def log_message(self, format, *args):
        # Suppress default logging
        pass

    def do_GET(self):
        # Parse query parameters
        if "?" in self.path:
            query_string = self.path.split("?", 1)[1]
            params = urllib.parse.parse_qs(query_string)

            # Verify state parameter
            server: AuthHTTPServer = self.server  # type: ignore
            if "state" in params and params["state"][0] == server.state:
                if "api_key" in params:
                    api_key = params["api_key"][0]
                    # Store the API key in the server instance
                    server.api_key = api_key
                    print("\n✓ Authentication successful!")

                    # Send success response with nice styling
                    self.send_response(200)
                    self.send_header("Content-type", "text/html")
                    self.end_headers()
                    self.wfile.write(b"""
                    <html>
                    <head>
                        <title>Omnara CLI - Authentication Successful</title>
                        <meta http-equiv="refresh" content="1;url=https://omnara.com/dashboard">
                        <style>
                            body {
                                margin: 0;
                                padding: 0;
                                min-height: 100vh;
                                background: linear-gradient(135deg, #1a1618 0%, #2a1f3d 100%);
                                font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
                                display: flex;
                                align-items: center;
                                justify-content: center;
                                color: #fef3c7;
                            }
                            .card {
                                background: rgba(26, 22, 24, 0.8);
                                border: 1px solid rgba(245, 158, 11, 0.2);
                                border-radius: 12px;
                                padding: 48px;
                                text-align: center;
                                box-shadow: 0 20px 40px rgba(0, 0, 0, 0.3),
                                           0 0 60px rgba(245, 158, 11, 0.1);
                                max-width: 400px;
                                animation: fadeIn 0.5s ease-out;
                            }
                            @keyframes fadeIn {
                                from { opacity: 0; transform: translateY(20px); }
                                to { opacity: 1; transform: translateY(0); }
                            }
                            .icon {
                                width: 64px;
                                height: 64px;
                                margin: 0 auto 24px;
                                background: rgba(134, 239, 172, 0.2);
                                border-radius: 50%;
                                display: flex;
                                align-items: center;
                                justify-content: center;
                                animation: scaleIn 0.5s ease-out 0.2s both;
                            }
                            @keyframes scaleIn {
                                from { transform: scale(0); }
                                to { transform: scale(1); }
                            }
                            .checkmark {
                                width: 32px;
                                height: 32px;
                                stroke: #86efac;
                                stroke-width: 3;
                                fill: none;
                                stroke-dasharray: 100;
                                stroke-dashoffset: 100;
                                animation: draw 0.5s ease-out 0.5s forwards;
                            }
                            @keyframes draw {
                                to { stroke-dashoffset: 0; }
                            }
                            h1 {
                                margin: 0 0 16px;
                                font-size: 24px;
                                font-weight: 600;
                                color: #86efac;
                            }
                            p {
                                margin: 0;
                                opacity: 0.8;
                                line-height: 1.5;
                            }
                            .close-hint {
                                margin-top: 24px;
                                font-size: 14px;
                                opacity: 0.6;
                            }
                        </style>
                    </head>
                    <body>
                        <div class="card">
                            <div class="icon">
                                <svg class="checkmark" viewBox="0 0 24 24">
                                    <path d="M20 6L9 17l-5-5" />
                                </svg>
                            </div>
                            <h1>Authentication Successful!</h1>
                            <p>Your CLI has been authorized to access Omnara.</p>
                            <p class="close-hint">Redirecting to dashboard in a moment...</p>
                            <p style="margin-top: 20px; font-size: 12px;">
                                If you are not redirected automatically,
                                <a href="https://omnara.com/dashboard" style="color: #86efac;">click here</a>.
                            </p>
                        </div>
                        <script>
                            setTimeout(() => {
                                window.location.href = 'https://omnara.com/dashboard';
                            }, 500);
                        </script>
                    </body>
                    </html>
                    """)
                    # Give the browser time to receive the response
                    self.wfile.flush()
                    return
            else:
                # Invalid or missing state parameter
                self.send_response(403)
                self.send_header("Content-type", "text/html")
                self.end_headers()
                self.wfile.write(b"""
                <html>
                <head><title>Omnara CLI - Authentication Failed</title></head>
                <body style="font-family: sans-serif; text-align: center; padding: 50px;">
                    <h1>Authentication Failed</h1>
                    <p>Invalid authentication state. Please try again.</p>
                </body>
                </html>
                """)
                return

        # Send error response
        self.send_response(400)
        self.send_header("Content-type", "text/html")
        self.end_headers()
        self.wfile.write(b"""
        <html>
        <head><title>Omnara CLI - Authentication Failed</title></head>
        <body style="font-family: sans-serif; text-align: center; padding: 50px;">
            <h1>Authentication Failed</h1>
            <p>No API key was received. Please try again.</p>
        </body>
        </html>
        """)


def authenticate_via_browser(auth_url="https://omnara.com"):
    """Authenticate via browser and return the API key"""

    # Generate a secure random state parameter
    state = secrets.token_urlsafe(32)

    # Start local server to receive the callback
    server = AuthHTTPServer(("localhost", 0), AuthCallbackHandler)
    server.state = state
    server.api_key = None
    port = server.server_port

    # Construct the auth URL
    auth_base = auth_url.rstrip("/")
    callback_url = f"http://localhost:{port}"
    auth_url = f"{auth_base}/cli-auth?callback={urllib.parse.quote(callback_url)}&state={urllib.parse.quote(state)}"

    print("\nOpening browser for authentication...")
    print("If your browser doesn't open automatically, please click this link:")
    print(f"\n  {auth_url}\n")
    print("Waiting for authentication...")

    # Run server in a thread
    server_thread = threading.Thread(target=server.serve_forever)
    server_thread.daemon = True
    server_thread.start()

    # Open browser
    try:
        webbrowser.open(auth_url)
    except Exception:
        pass

    # Wait for authentication (with timeout)
    start_time = time.time()
    while not server.api_key and (time.time() - start_time) < 300:
        time.sleep(0.1)

    # If we got the API key, wait a bit for the browser to process the redirect
    if server.api_key:
        time.sleep(1.5)  # Give browser time to receive response and start redirect

    # Shutdown server in a separate thread to avoid deadlock
    def shutdown_server():
        server.shutdown()

    shutdown_thread = threading.Thread(target=shutdown_server)
    shutdown_thread.start()
    shutdown_thread.join(timeout=1)  # Wait max 1 second for shutdown

    server.server_close()

    if server.api_key:
        return server.api_key
    else:
        raise Exception("Authentication failed - no API key received")


def ensure_api_key(args):
    """Ensure API key is available, authenticate if needed"""
    # Check if API key is provided via argument
    if hasattr(args, "api_key") and args.api_key:
        return args.api_key

    # Try to load from storage
    api_key = load_stored_api_key()
    if api_key:
        return api_key

    # Authenticate via browser
    print("No API key found. Starting authentication...")
    auth_url = getattr(args, "auth_url", "https://omnara.com")
    try:
        api_key = authenticate_via_browser(auth_url)
        save_api_key(api_key)
        print("Authentication successful! API key saved.")
        return api_key
    except Exception as e:
        raise Exception(f"Authentication failed: {str(e)}")


def run_claude_chat(args, unknown_args):
    """Run the Claude chat integration (default behavior)"""
    api_key = ensure_api_key(args)

    # Import and run directly instead of subprocess
    from webhooks.claude_wrapper_v3 import main as claude_wrapper_main

    # Prepare sys.argv for the claude wrapper
    original_argv = sys.argv
    new_argv = ["claude_wrapper_v3", "--api-key", api_key]

    if hasattr(args, "base_url") and args.base_url:
        new_argv.extend(["--base-url", args.base_url])

    # Add any additional Claude arguments
    if unknown_args:
        new_argv.extend(unknown_args)

    try:
        sys.argv = new_argv
        claude_wrapper_main()
    finally:
        sys.argv = original_argv


def cmd_serve(args):
    """Handle the 'serve' subcommand"""
    # Run the webhook server with appropriate tunnel configuration
    cmd = [
        sys.executable,
        "-m",
        "webhooks.claude_code",
    ]

    if args.skip_permissions:
        cmd.append("--dangerously-skip-permissions")

    # Handle tunnel configuration
    if not args.no_tunnel:
        # Default: use Cloudflare tunnel
        cmd.append("--cloudflare-tunnel")
        print("[INFO] Starting webhook server with Cloudflare tunnel...")
    else:
        # Local only, no tunnel
        print("[INFO] Starting local webhook server (no tunnel)...")

    if args.port is not None:
        cmd.extend(["--port", str(args.port)])

    subprocess.run(cmd)


def cmd_mcp(args):
    """Handle the 'mcp' subcommand"""

    cmd = [
        sys.executable,
        "-m",
        "servers.mcp_server.stdio_server",
    ]

    if args.base_url:
        cmd.extend(["--api-key", args.api_key])
    if args.base_url:
        cmd.extend(["--base-url", args.base_url])
    if args.permission_tool:
        cmd.append("--claude-code-permission-tool")
    if args.git_diff:
        cmd.append("--git-diff")
    if args.agent_instance_id:
        cmd.extend(["--agent-instance-id", args.agent_instance_id])

    subprocess.run(cmd)


def add_global_arguments(parser):
    """Add global arguments that work across all subcommands"""
    parser.add_argument(
        "--auth",
        action="store_true",
        help="Authenticate or re-authenticate with Omnara",
    )
    parser.add_argument(
        "--reauth",
        action="store_true",
        help="Force re-authentication even if API key exists",
    )
    parser.add_argument(
        "--version", action="store_true", help="Show version information"
    )
    parser.add_argument(
        "--api-key", help="API key for authentication (uses stored key if not provided)"
    )
    parser.add_argument(
        "--base-url",
        default="https://agent-dashboard-mcp.onrender.com",
        help="Base URL of the Omnara API server",
    )
    parser.add_argument(
        "--auth-url",
        default="https://omnara.com",
        help="Base URL of the Omnara frontend for authentication",
    )


def main():
    """Main entry point with subcommand support"""
    # Create main parser
    parser = argparse.ArgumentParser(
        description="Omnara - AI Agent Dashboard and Tools",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Start Claude chat (default)
  omnara
  omnara --api-key YOUR_API_KEY

  # Start webhook server with Cloudflare tunnel
  omnara serve

  # Start local webhook server (no tunnel)
  omnara serve --no-tunnel
  omnara serve --no-tunnel --port 8080

  # Run MCP stdio server
  omnara mcp
  omnara mcp --git-diff

  # Authenticate
  omnara --auth

  # Show version
  omnara --version
        """,
    )

    # Add global arguments
    add_global_arguments(parser)

    # Create subparsers
    subparsers = parser.add_subparsers(dest="command", help="Available commands")

    # 'serve' subcommand
    serve_parser = subparsers.add_parser(
        "serve", help="Start webhook server for Claude Code integration"
    )
    serve_parser.add_argument(
        "--no-tunnel",
        action="store_true",
        help="Run locally without tunnel (default: uses Cloudflare tunnel)",
    )
    serve_parser.add_argument(
        "--port", type=int, help="Port to run the webhook server on (default: 6662)"
    )
    serve_parser.add_argument(
        "--skip-permissions",
        action="store_true",
        help="Skip permission prompts in Claude Code - USE WITH CAUTION",
    )

    # 'mcp' subcommand
    mcp_parser = subparsers.add_parser("mcp", help="Run MCP stdio server")
    mcp_parser.add_argument(
        "--permission-tool",
        action="store_true",
        help="Enable Claude Code permission prompt tool",
    )
    mcp_parser.add_argument(
        "--git-diff",
        action="store_true",
        help="Enable git diff capture for log_step and ask_question",
    )
    mcp_parser.add_argument(
        "--agent-instance-id",
        type=str,
        help="Pre-existing agent instance ID to use for this session",
    )
    mcp_parser.add_argument(
        "--api-key",
        type=str,
        help="API key to use for the MCP server",
    )

    # Parse arguments
    args, unknown_args = parser.parse_known_args()

    # Handle version flag
    if args.version:
        print(f"omnara version {get_current_version()}")
        sys.exit(0)

    # Handle auth flag
    if args.auth or args.reauth:
        try:
            if args.reauth:
                print("Re-authenticating...")
            else:
                print("Starting authentication...")
            api_key = authenticate_via_browser(args.auth_url)
            save_api_key(api_key)
            print("Authentication successful! API key saved.")
            sys.exit(0)
        except Exception as e:
            print(f"Authentication failed: {str(e)}")
            sys.exit(1)

    # Check for updates
    check_for_updates()

    # Handle subcommands
    if args.command == "serve":
        cmd_serve(args)
    elif args.command == "mcp":
        cmd_mcp(args)
    else:
        # Default behavior: run Claude chat
        run_claude_chat(args, unknown_args)


if __name__ == "__main__":
    main()
