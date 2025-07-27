"""Authentication module for Omnara CLI"""

import json
import os
import random
import string
import time
import webbrowser
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from threading import Thread
from urllib.parse import parse_qs, urlparse
import logging
import requests
from typing import Optional

logger = logging.getLogger(__name__)

# Config file location
CONFIG_DIR = Path.home() / ".omnara"
CONFIG_FILE = CONFIG_DIR / "config.json"


class AuthHTTPServer(HTTPServer):
    """Custom HTTPServer with auth_code attribute"""

    auth_code: Optional[str] = None


class AuthCallbackHandler(BaseHTTPRequestHandler):
    """HTTP handler for OAuth callback"""

    server: AuthHTTPServer  # Type hint for the server attribute

    def do_GET(self):
        """Handle the OAuth callback"""
        parsed_path = urlparse(self.path)
        query = parse_qs(parsed_path.query)

        if "code" in query:
            # Store the auth code for the main thread to access
            self.server.auth_code = query["code"][0]

            # Send success response
            self.send_response(200)
            self.send_header("Content-type", "text/html")
            self.end_headers()
            self.wfile.write(b"""
                <html>
                <head>
                    <title>Authentication Successful</title>
                    <style>
                        body { font-family: Arial, sans-serif; text-align: center; padding: 50px; }
                        .success { color: green; font-size: 24px; }
                    </style>
                </head>
                <body>
                    <h1 class="success">Authentication Successful!</h1>
                    <p>You can close this window and return to your terminal.</p>
                </body>
                </html>
            """)
        else:
            # Error response
            self.send_response(400)
            self.send_header("Content-type", "text/html")
            self.end_headers()
            self.wfile.write(b"""
                <html>
                <head>
                    <title>Authentication Failed</title>
                    <style>
                        body { font-family: Arial, sans-serif; text-align: center; padding: 50px; }
                        .error { color: red; font-size: 24px; }
                    </style>
                </head>
                <body>
                    <h1 class="error">Authentication Failed</h1>
                    <p>Please try again.</p>
                </body>
                </html>
            """)

    def log_message(self, format, *args):
        """Suppress logging"""
        pass


def generate_state():
    """Generate a random state parameter for OAuth"""
    return "".join(random.choices(string.ascii_letters + string.digits, k=32))


def login(base_url=None):
    """Perform OAuth login flow"""
    if base_url is None:
        base_url = "https://agent-dashboard-mcp.onrender.com"

    # Start local server for callback
    server = AuthHTTPServer(("localhost", 8765), AuthCallbackHandler)
    server.auth_code = None
    server.timeout = 120  # 2 minute timeout

    # Generate state for CSRF protection
    state = generate_state()

    # Build auth URL
    redirect_uri = "http://localhost:8765/callback"
    auth_url = f"{base_url}/cli-auth?redirect_uri={redirect_uri}&state={state}"

    print("[INFO] Opening browser for authentication...")
    print(f"[INFO] If browser doesn't open, visit: {auth_url}")

    # Open browser
    webbrowser.open(auth_url)

    # Handle the callback in a separate thread
    def handle_request():
        server.handle_request()

    thread = Thread(target=handle_request)
    thread.daemon = True
    thread.start()

    # Wait for auth code
    print("[INFO] Waiting for authentication...")
    start_time = time.time()
    while server.auth_code is None and time.time() - start_time < 120:
        time.sleep(0.5)

    server.server_close()

    if server.auth_code is None:
        print("[ERROR] Authentication timed out")
        return False

    # Exchange auth code for API token
    try:
        response = requests.post(
            f"{base_url}/auth/cli/exchange-token",
            json={"auth_code": server.auth_code},
            timeout=30,
        )
        response.raise_for_status()

        token_data = response.json()
        api_key = token_data["api_key"]

        # Save to config
        save_config({"api_key": api_key, "base_url": base_url})

        print("[SUCCESS] Authentication successful!")
        print(f"[INFO] Credentials saved to {CONFIG_FILE}")
        return True

    except requests.exceptions.RequestException as e:
        print(f"[ERROR] Failed to exchange auth code: {e}")
        return False


def logout():
    """Remove stored credentials"""
    if CONFIG_FILE.exists():
        CONFIG_FILE.unlink()
        print("[INFO] Logged out successfully")
    else:
        print("[INFO] No credentials found")


def get_stored_credentials():
    """Get stored API key and base URL from config"""
    if not CONFIG_FILE.exists():
        return None, None

    try:
        with open(CONFIG_FILE, "r") as f:
            config = json.load(f)
            return config.get("api_key"), config.get("base_url")
    except Exception:
        return None, None


def save_config(config):
    """Save configuration to file"""
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)

    # Set restrictive permissions (owner read/write only)
    with open(CONFIG_FILE, "w") as f:
        json.dump(config, f, indent=2)

    # Set file permissions to 600 (owner read/write only)
    os.chmod(CONFIG_FILE, 0o600)


def is_logged_in():
    """Check if user is logged in"""
    api_key, _ = get_stored_credentials()
    return api_key is not None
