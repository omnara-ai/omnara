import argparse
import asyncio
import os
import platform
import re
import secrets
import select
import subprocess
import sys
import time
import uuid
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Dict, List, Optional, Tuple

import uvicorn
from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import JSONResponse
from pydantic import BaseModel, field_validator

from integrations.headless.claude_code import HeadlessClaudeRunner


# === CONSTANTS AND CONFIGURATION ===
DEFAULT_PORT = 6662
DEFAULT_HOST = "0.0.0.0"

# Cache for command paths to avoid repeated lookups
COMMAND_PATHS = {}

# === DEPENDENCY CHECKING ===
REQUIRED_COMMANDS = {
    "git": "Git is required for creating worktrees",
    "claude": "Claude Code CLI is required",
}

OPTIONAL_COMMANDS = {"cloudflared": "Cloudflared is optional for tunnel support"}


def is_macos() -> bool:
    """Check if running on macOS"""
    return platform.system() == "Darwin"


def get_command_path(command: str) -> Optional[str]:
    """Get the full path to a command, using cache if available"""
    if command in COMMAND_PATHS:
        return COMMAND_PATHS[command]

    exists, path = check_command(command)
    if exists and path:
        COMMAND_PATHS[command] = path
        return path

    return None


def check_command(command: str) -> Tuple[bool, Optional[str]]:
    """Check if a command exists and return its path"""
    try:
        # First try without shell (more secure, finds actual executables)
        result = subprocess.run(["which", command], capture_output=True, text=True)
        if result.returncode == 0:
            return True, result.stdout.strip()

        # If that fails, try with shell to catch aliases (less secure but necessary for aliases)
        shell_result = subprocess.run(
            f"which {command}",
            shell=True,
            capture_output=True,
            text=True,
            executable="/bin/bash",  # Use bash to ensure consistent behavior
        )
        if shell_result.returncode == 0:
            path = shell_result.stdout.strip()
            # For aliases, extract the actual path if possible
            if "aliased to" in path:
                # Extract path from "claude: aliased to /path/to/claude"
                parts = path.split("aliased to")
                if len(parts) > 1:
                    actual_path = parts[1].strip()
                    # Verify the extracted path exists
                    if os.path.exists(actual_path):
                        return True, actual_path
            return True, path

        return False, None
    except Exception:
        return False, None


def try_install_with_brew(command: str) -> bool:
    """Try to install a command with brew on macOS"""
    if not is_macos():
        return False

    # Check if brew is available
    brew_path = get_command_path("brew")
    if not brew_path:
        return False

    print(f"[INFO] Attempting to install {command} with Homebrew...")
    try:
        result = subprocess.run(
            [brew_path, "install", command],
            capture_output=True,
            text=True,
            timeout=300,  # 5 minute timeout for brew install
        )
        if result.returncode == 0:
            print(f"[SUCCESS] {command} installed successfully with Homebrew")
            return True
        else:
            print(f"[ERROR] Failed to install {command} with Homebrew: {result.stderr}")
            return False
    except subprocess.TimeoutExpired:
        print(f"[ERROR] Homebrew installation of {command} timed out")
        return False
    except Exception as e:
        print(f"[ERROR] Failed to install {command} with Homebrew: {e}")
        return False


def check_dependencies() -> List[str]:
    """Check all required dependencies and return list of errors"""
    errors = []
    for cmd, description in REQUIRED_COMMANDS.items():
        exists, _ = check_command(cmd)
        if not exists:
            # Add error message with platform-specific hints
            if is_macos():
                brew_exists, _ = check_command("brew")
                if brew_exists:
                    errors.append(
                        f"{description}. You can install it with: brew install {cmd}"
                    )
                else:
                    errors.append(f"{description}. Please install {cmd}.")
            else:
                errors.append(f"{description}. Please install {cmd}.")
    return errors


def get_command_status() -> Dict[str, bool]:
    """Get status of all commands (required and optional)"""
    status = {}
    for cmd in {**REQUIRED_COMMANDS, **OPTIONAL_COMMANDS}:
        exists, _ = check_command(cmd)
        status[cmd] = exists
    return status


# === ENVIRONMENT VALIDATION ===
def is_git_repository(path: str = ".") -> bool:
    """Check if the given path is within a git repository"""
    git_path = get_command_path("git")
    if not git_path:
        return False

    result = subprocess.run(
        [git_path, "rev-parse", "--git-dir"], capture_output=True, text=True, cwd=path
    )
    return result.returncode == 0


def check_worktree_exists(worktree_name: str) -> Tuple[bool, Optional[str]]:
    """Check if a worktree with the given name exists and return its path"""
    try:
        git_path = get_command_path("git")
        if not git_path:
            return False, None

        result = subprocess.run(
            [git_path, "worktree", "list"], capture_output=True, text=True, check=True
        )

        # Parse worktree list output
        # Format: /path/to/worktree branch-name [branch-ref]
        for line in result.stdout.strip().split("\n"):
            if line:
                parts = line.split()
                if len(parts) >= 2:
                    path = parts[0]
                    # Extract worktree name from path
                    dirname = os.path.basename(path)
                    if dirname == worktree_name:
                        return True, path

        return False, None
    except subprocess.CalledProcessError:
        return False, None


def validate_environment() -> List[str]:
    """Validate the environment is suitable for running the webhook"""
    errors = []

    if not is_git_repository():
        errors.append(
            "Not running in a git repository. The webhook must be started from within a git repository."
        )

    # Check if git worktree command exists
    if is_git_repository():
        git_path = get_command_path("git")
        if git_path:
            result = subprocess.run(
                [git_path, "worktree", "list"],
                capture_output=True,
                text=True,
            )
            if result.returncode != 0:
                errors.append(
                    f"Git worktree command not available: {result.stderr.strip()}"
                )
        else:
            errors.append("Git command not found")

    return errors


# === CLOUDFLARE TUNNEL MANAGEMENT ===
def check_cloudflared_installed() -> bool:
    """Check if cloudflared is available"""
    cloudflared_path = get_command_path("cloudflared")
    if not cloudflared_path:
        return False

    try:
        subprocess.run([cloudflared_path, "--version"], capture_output=True, check=True)
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False


def start_cloudflare_tunnel(
    port: int = DEFAULT_PORT,
) -> Tuple[Optional[subprocess.Popen], Optional[str]]:
    """Start Cloudflare tunnel and return the process and tunnel URL"""
    if not check_cloudflared_installed():
        # Try to install with brew on macOS
        if is_macos() and try_install_with_brew("cloudflared"):
            # Check again after installation
            if not check_cloudflared_installed():
                print("\n[ERROR] cloudflared installation failed!")
                print(
                    "Please install cloudflared manually to use the --cloudflare-tunnel option."
                )
                return None, None
        else:
            print("\n[ERROR] cloudflared is not installed!")
            if is_macos():
                brew_exists, _ = check_command("brew")
                if brew_exists:
                    print("You can install it with: brew install cloudflared")
                else:
                    print(
                        "Please install cloudflared to use the --cloudflare-tunnel option."
                    )
            else:
                print(
                    "Please install cloudflared to use the --cloudflare-tunnel option."
                )
            print(
                "Visit: https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/"
            )
            print("for installation instructions.")
            return None, None

    print("[INFO] Starting Cloudflare tunnel...")
    try:
        cloudflared_path = get_command_path("cloudflared")
        if not cloudflared_path:
            print("\n[ERROR] cloudflared path not found")
            return None, None

        # Start cloudflared with output capture
        process = subprocess.Popen(
            [cloudflared_path, "tunnel", "--url", f"http://localhost:{port}"],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            bufsize=1,
        )

        # Wait for tunnel URL to appear in output
        tunnel_url = None
        start_time = time.time()
        timeout = 10  # seconds

        while time.time() - start_time < timeout:
            if process.poll() is not None:
                print("\n[ERROR] Cloudflare tunnel process exited unexpectedly")
                return None, None

            # Check stderr (cloudflared outputs to stderr)
            try:
                # Read available lines from stderr
                if process.stderr:
                    readable, _, _ = select.select([process.stderr], [], [], 0.1)
                    if readable:
                        line = process.stderr.readline()
                        if line:
                            # Look for the tunnel URL pattern
                            url_match = re.search(
                                r"https://[a-zA-Z0-9-]+\.trycloudflare\.com", line
                            )
                            if url_match:
                                tunnel_url = url_match.group()
                                break
            except Exception:
                pass

        if not tunnel_url:
            print("\n[WARNING] Could not parse tunnel URL from cloudflared output")
            print("[INFO] Cloudflare tunnel started but URL not captured")
        else:
            print("[INFO] Cloudflare tunnel started successfully")

        return process, tunnel_url
    except Exception as e:
        print(f"\n[ERROR] Failed to start Cloudflare tunnel: {e}")
        return None, None


class WebhookRequest(BaseModel):
    agent_instance_id: str
    prompt: str  # Initial prompt to pass to Claude
    name: str | None = None  # Branch name
    worktree_name: str | None = None
    agent_type: str | None = None  # Agent type name

    @field_validator("agent_instance_id")
    def validate_instance_id(cls, v):
        try:
            uuid.UUID(v)
            return v
        except ValueError:
            raise ValueError("Invalid UUID format for agent_instance_id")

    @field_validator("name")
    def validate_name(cls, v):
        if v is not None:
            if not re.match(r"^[a-zA-Z0-9-]+$", v):
                raise ValueError(
                    "Branch name must contain only letters, numbers, and hyphens"
                )
            if len(v) > 50:
                raise ValueError("Branch name must be 50 characters or less")
        return v

    @field_validator("worktree_name")
    def validate_worktree_name(cls, v):
        if v is not None:
            if not re.match(r"^[a-zA-Z0-9-]+$", v):
                raise ValueError(
                    "Worktree name must contain only letters, numbers, and hyphens"
                )
            if len(v) > 100:
                raise ValueError("Worktree name must be 100 characters or less")
        return v


@asynccontextmanager
async def lifespan(app: FastAPI):
    # Run startup checks
    print("[INFO] Running startup checks...")

    # Check dependencies
    dep_errors = check_dependencies()
    env_errors = validate_environment()

    if dep_errors or env_errors:
        print("\n[ERROR] Startup checks failed:")
        for error in dep_errors + env_errors:
            print(f"  - {error}")
        print("\n[ERROR] Please fix these issues before starting the webhook server.")
        sys.exit(1)

    # Show command availability
    status = get_command_status()
    print("\n[INFO] Command availability:")
    for cmd, available in status.items():
        required = cmd in REQUIRED_COMMANDS
        status_icon = "✓" if available else "✗"
        req_label = " (required)" if required else " (optional)"
        print(f"  - {cmd}: {status_icon}{req_label}")

    print("\n[INFO] All required checks passed")

    # Initialize storage for running Claude processes
    app.state.running_processes = {}

    # Handle Cloudflare tunnel if requested
    tunnel_url = None
    if hasattr(app.state, "cloudflare_tunnel") and app.state.cloudflare_tunnel:
        port = getattr(app.state, "port", DEFAULT_PORT)
        tunnel_process, tunnel_url = start_cloudflare_tunnel(port=port)
        app.state.tunnel_process = tunnel_process
        if not tunnel_process:
            print("[WARNING] Continuing without Cloudflare tunnel")

    # Set up webhook secret
    secret = os.environ.get("CLAUDE_WEBHOOK_SECRET")
    if not secret:
        secret = secrets.token_urlsafe(12)

    app.state.webhook_secret = secret

    # Initialize extra_args if not already set (when run via uvicorn directly)
    if not hasattr(app.state, "extra_args"):
        app.state.extra_args = {}

    # Display webhook info in a prominent box
    box_width = 90
    print("\n" + "╔" + "═" * box_width + "╗")
    print("║" + " " * box_width + "║")

    # Format the header
    header = "AGENT CONFIGURATION"
    header_padding = (box_width - len(header)) // 2
    print(
        "║"
        + " " * header_padding
        + header
        + " " * (box_width - header_padding - len(header))
        + "║"
    )

    # Add instruction text
    instruction = "(paste this information into Omnara)"
    instruction_padding = (box_width - len(instruction)) // 2
    print(
        "║"
        + " " * instruction_padding
        + instruction
        + " " * (box_width - instruction_padding - len(instruction))
        + "║"
    )
    print("║" + " " * box_width + "║")

    # Display tunnel URL first if available
    if tunnel_url:
        url_line = f"  Webhook URL: {tunnel_url}"
        print("║" + url_line + " " * (box_width - len(url_line)) + "║")
        print("║" + " " * box_width + "║")
    elif hasattr(app.state, "cloudflare_tunnel") and app.state.cloudflare_tunnel:
        cf_line = "  Webhook URL: (waiting for cloudflared to provide URL...)"
        print("║" + cf_line + " " * (box_width - len(cf_line)) + "║")
        print("║" + " " * box_width + "║")

    # Format the API key line with proper padding
    api_key_line = f"  API Key: {secret}"
    print("║" + api_key_line + " " * (box_width - len(api_key_line)) + "║")

    print("║" + " " * box_width + "║")
    print("╚" + "═" * box_width + "╝")

    yield

    # Cleanup
    if hasattr(app.state, "tunnel_process") and app.state.tunnel_process:
        print("\n[INFO] Stopping Cloudflare tunnel...")
        app.state.tunnel_process.terminate()
        app.state.tunnel_process.wait()

    # Stop all running Claude tasks
    if hasattr(app.state, "running_processes"):
        # Create a list of tasks to cancel (avoid modifying dict during iteration)
        tasks_to_cancel = []
        for instance_id, process_info in list(app.state.running_processes.items()):
            print(f"\n[INFO] Stopping Claude session for instance {instance_id}...")
            if "task" in process_info:
                task = process_info["task"]
                if not task.done():
                    tasks_to_cancel.append((instance_id, task))

        # Cancel all tasks and wait for them to finish
        for instance_id, task in tasks_to_cancel:
            task.cancel()

        # Wait for all tasks to complete cleanup
        if tasks_to_cancel:
            await asyncio.gather(
                *[task for _, task in tasks_to_cancel],
                return_exceptions=True,  # Don't raise exceptions from cancelled tasks
            )

    if hasattr(app.state, "webhook_secret"):
        delattr(app.state, "webhook_secret")


app = FastAPI(lifespan=lifespan)


@app.exception_handler(Exception)
async def general_exception_handler(request: Request, exc: Exception):
    print(f"[ERROR] Unhandled exception: {str(exc)}")
    print(f"[ERROR] Exception type: {type(exc).__name__}")
    import traceback

    traceback.print_exc()
    return JSONResponse(
        status_code=500, content={"detail": f"Internal server error: {str(exc)}"}
    )


def verify_auth(request: Request, authorization: str = Header(None)) -> bool:
    """Verify the authorization header contains the correct secret"""

    if not authorization:
        return False

    parts = authorization.split()
    if len(parts) != 2 or parts[0].lower() != "bearer":
        return False

    provided_secret = parts[1]
    expected_secret = getattr(request.app.state, "webhook_secret", None)

    if not expected_secret:
        return False

    is_valid = secrets.compare_digest(provided_secret, expected_secret)
    return is_valid


@app.post("/")
async def start_claude(
    request: Request,
    webhook_data: WebhookRequest,
    authorization: str = Header(None),
    x_omnara_api_key: str = Header(None, alias="X-Omnara-Api-Key"),
):
    try:
        if not verify_auth(request, authorization):
            print("[ERROR] Invalid or missing authorization")
            raise HTTPException(
                status_code=401, detail="Invalid or missing authorization"
            )

        agent_instance_id = webhook_data.agent_instance_id
        prompt = webhook_data.prompt
        worktree_name = webhook_data.worktree_name
        branch_name = webhook_data.name
        agent_type = webhook_data.agent_type

        print("\n[INFO] Received webhook request:")
        print(f"  - Instance ID: {agent_instance_id}")
        print(f"  - Worktree name: {worktree_name or 'auto-generated'}")
        print(f"  - Branch name: {branch_name or 'current branch'}")
        print(f"  - Agent type: {agent_type or 'default'}")
        print(f"  - Prompt length: {len(prompt)} characters")

        # Determine worktree/branch name
        if worktree_name:
            # Special case: if worktree_name is 'main', use current directory
            if worktree_name == "main":
                work_dir = os.path.abspath(".")
                feature_branch_name = branch_name if branch_name else "main"
                create_new_worktree = False
                print("\n[INFO] Using current directory (no worktree)")
                print(f"  - Directory: {work_dir}")
                if branch_name and branch_name != "main":
                    print(f"  - Will checkout branch: {branch_name}")
                print(
                    "\n[WARNING] Using main worktree - parallel sessions may cause file conflicts"
                )
            else:
                # Check if worktree already exists
                exists, existing_path = check_worktree_exists(worktree_name)
                if exists and existing_path:
                    # Use existing worktree
                    work_dir = os.path.abspath(existing_path)
                    feature_branch_name = branch_name if branch_name else worktree_name
                    create_new_worktree = False
                    print(f"\n[INFO] Using existing worktree: {worktree_name}")
                    print(f"  - Directory: {work_dir}")
                    if branch_name:
                        print(f"  - Will checkout branch: {branch_name}")
                else:
                    # Create new worktree with specified name
                    feature_branch_name = branch_name if branch_name else worktree_name
                    work_dir = os.path.abspath(f"./{worktree_name}")
                    create_new_worktree = True
                    print(f"\n[INFO] Creating new worktree: {worktree_name}")
                    if branch_name:
                        print(f"  - With branch: {branch_name}")
        else:
            # Auto-generate name with timestamp
            now = datetime.now()
            timestamp_str = now.strftime("%Y%m%d%H%M%S")
            safe_timestamp = re.sub(r"[^a-zA-Z0-9-]", "", timestamp_str)
            feature_branch_name = f"omnara-claude-{safe_timestamp}"
            work_dir = os.path.abspath(f"./{feature_branch_name}")
            create_new_worktree = True
            print(
                f"\n[INFO] Creating new worktree with auto-generated name: {feature_branch_name}"
            )
        base_dir = os.path.abspath(".")

        if not work_dir.startswith(base_dir):
            print(f"[ERROR] Invalid working directory: {work_dir} not under {base_dir}")
            raise HTTPException(status_code=400, detail="Invalid working directory")

        # Additional runtime check for git repository
        if not is_git_repository(base_dir):
            print(f"[ERROR] Not in a git repository. Current directory: {base_dir}")
            raise HTTPException(
                status_code=500,
                detail="Server is not running in a git repository. Please start the webhook from within a git repository.",
            )

        if create_new_worktree:
            print("\n[INFO] Creating git worktree:")
            print(f"  - Branch: {feature_branch_name}")
            print(f"  - Directory: {work_dir}")

            # Get git path
            git_path = get_command_path("git")

            if not git_path:
                print("[ERROR] Git command not found in PATH or as alias")
                raise HTTPException(
                    status_code=500,
                    detail="Git command not found. Please ensure git is installed and in PATH.",
                )

            # First check if the branch already exists
            branch_check = subprocess.run(
                [
                    git_path,
                    "rev-parse",
                    "--verify",
                    f"refs/heads/{feature_branch_name}",
                ],
                capture_output=True,
                text=True,
                cwd=base_dir,
            )

            if branch_check.returncode == 0:
                # Branch exists, add worktree without -b flag
                cmd = [git_path, "worktree", "add", work_dir, feature_branch_name]

            else:
                # Branch doesn't exist, create it with -b flag
                cmd = [git_path, "worktree", "add", work_dir, "-b", feature_branch_name]

            result = subprocess.run(
                cmd,
                capture_output=True,
                text=True,
                timeout=30,
                cwd=base_dir,
            )

            if result.returncode != 0:
                print("\n[ERROR] Git worktree creation failed:")
                print(f"  - Command: {' '.join(cmd)}")
                print(f"  - Exit code: {result.returncode}")
                print(f"  - stdout: {result.stdout}")
                print(f"  - stderr: {result.stderr}")

                # Provide more helpful error messages
                error_detail = result.stderr
                if "not a git repository" in result.stderr:
                    error_detail = "Not in a git repository. The webhook must be started from within a git repository."
                elif "already exists" in result.stderr:
                    error_detail = f"Branch or worktree '{feature_branch_name}' already exists. Try again with a different name."
                elif "Permission denied" in result.stderr:
                    error_detail = "Permission denied. Check directory permissions."

                raise HTTPException(
                    status_code=500, detail=f"Failed to create worktree: {error_detail}"
                )
        else:
            # Not creating a new worktree, but may need to checkout a branch
            if branch_name and branch_name != feature_branch_name:
                print(f"\n[INFO] Checking out branch: {branch_name}")

                # Get git path
                git_path = get_command_path("git")
                if not git_path:
                    print("[ERROR] Git command not found in PATH or as alias")
                    raise HTTPException(
                        status_code=500,
                        detail="Git command not found. Please ensure git is installed and in PATH.",
                    )

                # First check if the branch exists
                branch_check = subprocess.run(
                    [git_path, "rev-parse", "--verify", f"refs/heads/{branch_name}"],
                    capture_output=True,
                    text=True,
                    cwd=work_dir,
                )

                if branch_check.returncode == 0:
                    # Branch exists, checkout

                    checkout_result = subprocess.run(
                        [git_path, "checkout", branch_name],
                        capture_output=True,
                        text=True,
                        cwd=work_dir,
                    )

                    if checkout_result.returncode != 0:
                        print(
                            f"[ERROR] Failed to checkout branch: {checkout_result.stderr}"
                        )
                        raise HTTPException(
                            status_code=500,
                            detail=f"Failed to checkout branch '{branch_name}': {checkout_result.stderr}",
                        )
                else:
                    # Branch doesn't exist, create and checkout
                    print(f"[INFO] Creating new branch: {branch_name}")

                    checkout_result = subprocess.run(
                        [git_path, "checkout", "-b", branch_name],
                        capture_output=True,
                        text=True,
                        cwd=work_dir,
                    )

                    if checkout_result.returncode != 0:
                        print(
                            f"[ERROR] Failed to create branch: {checkout_result.stderr}"
                        )
                        raise HTTPException(
                            status_code=500,
                            detail=f"Failed to create branch '{branch_name}': {checkout_result.stderr}",
                        )

        # Get Omnara API key from header
        if not x_omnara_api_key:
            print("[ERROR] Omnara API key missing from X-Omnara-Api-Key header")
            raise HTTPException(
                status_code=400,
                detail="Omnara API key required. Provide via X-Omnara-Api-Key header.",
            )
        omnara_api_key = x_omnara_api_key

        # No need to handle permission mode explicitly - it flows through extra_args

        print("\n[INFO] Starting Claude headless session:")
        print(f"  - Working directory: {work_dir}")
        print(f"  - Instance ID: {agent_instance_id}")
        print("  - MCP server: Omnara with API key")

        # Get extra args from app state if available
        extra_args = getattr(request.app.state, "extra_args", {})

        # Create HeadlessClaudeRunner instance
        # Note: The subprocess will inherit the current Python environment,
        # so it will use the same virtual environment if one is active
        # All CLI args (like --permission-mode) flow through extra_args
        runner = HeadlessClaudeRunner(
            omnara_api_key=omnara_api_key,
            session_id=agent_instance_id,
            omnara_base_url="https://agent-dashboard-mcp.onrender.com",
            initial_prompt=prompt,  # Pass the initial prompt from webhook
            extra_args=extra_args,
            cwd=work_dir,
            console_output=False,  # Disable console output when running from webhook
        )

        # Start the runner in the background
        async def run_claude_in_background():
            try:
                await runner.run()
            except asyncio.CancelledError:
                print(f"[INFO] Claude session {agent_instance_id} was cancelled")
                raise  # Re-raise to properly handle cancellation
            except Exception as e:
                print(f"[ERROR] Claude session {agent_instance_id} failed: {e}")
            finally:
                # Remove from running processes when done
                if hasattr(request.app.state, "running_processes"):
                    request.app.state.running_processes.pop(agent_instance_id, None)

        # Create a task for the background runner
        task = asyncio.create_task(run_claude_in_background())

        # Store the task and runner info
        request.app.state.running_processes[agent_instance_id] = {
            "task": task,
            "runner": runner,
            "work_dir": work_dir,
            "branch": feature_branch_name,
            "started_at": datetime.now().isoformat(),
        }

        # Give it a moment to initialize
        await asyncio.sleep(1)

        # Check if the task is still running
        if task.done():
            # Task failed immediately
            print("\n[ERROR] Claude session exited immediately")
            print("\n[ERROR] Possible causes:")
            print("  - Claude Code SDK not installed")
            print("  - MCP server (omnara) cannot be started")
            print("  - Invalid API key")
            print("  - Working directory issues")
            raise HTTPException(
                status_code=500,
                detail="Claude session started but exited immediately. Check server logs for details.",
            )

        print("\n[SUCCESS] Claude headless session started successfully!")
        print(f"  - Instance ID: {agent_instance_id}")
        print(f"  - Working directory: {work_dir}")
        print(f"  - Branch: {feature_branch_name}")
        print(f"  - Claude logs: ~/.omnara/claude_headless/{agent_instance_id}.log")

        return {
            "message": "Successfully started claude",
            "branch": feature_branch_name,
            "instance_id": agent_instance_id,
            "work_dir": work_dir,
        }

    except subprocess.TimeoutExpired:
        print("[ERROR] Git operation timed out")
        raise HTTPException(status_code=500, detail="Git operation timed out")
    except HTTPException:
        # Re-raise HTTP exceptions as-is
        raise
    except Exception as e:
        print(f"[ERROR] Failed to start claude: {str(e)}")
        print(f"[ERROR] Exception type: {type(e).__name__}")

        import traceback

        traceback.print_exc()

        raise HTTPException(status_code=500, detail=f"Failed to start claude: {str(e)}")


@app.get("/health")
async def health_check():
    """Health check endpoint - no auth required"""
    return {"status": "healthy"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Claude Code Webhook Server (Headless Mode)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run webhook server (NEW - recommended way)
  omnara serve

  # With permission mode
  omnara serve --permission-mode bypassPermissions

  # With multiple Claude CLI args
  omnara serve --permission-mode plan --allowed-tools Read,Write

  # Run directly (old way)
  python -m integrations.webhooks.claude_code.claude_code --cloudflare-tunnel

Note: This webhook runs Claude Code in headless mode without requiring GNU Screen.
All unrecognized arguments are passed through to Claude CLI (e.g., --permission-mode,
--dangerously-skip-permissions, --allowed-tools, etc.)
        """,
    )
    # Only handle webhook-specific args, everything else flows through
    parser.add_argument(
        "--cloudflare-tunnel",
        action="store_true",
        help="Start Cloudflare tunnel for external access",
    )
    parser.add_argument(
        "--port",
        type=int,
        default=DEFAULT_PORT,
        help=f"Port to run the webhook server on (default: {DEFAULT_PORT})",
    )

    # Parse known args and collect unknown args for passing to headless runner
    args, unknown_args = parser.parse_known_args()

    # Convert unknown arguments to extra_args dict for passing to HeadlessClaudeRunner
    extra_args = {}
    i = 0
    while i < len(unknown_args):
        arg = unknown_args[i]
        if arg.startswith("--"):
            key = arg[2:]  # Remove '--' prefix
            # Check if next argument is the value (doesn't start with '-')
            if i + 1 < len(unknown_args) and not unknown_args[i + 1].startswith("-"):
                extra_args[key] = unknown_args[i + 1]
                i += 2
            else:
                # Flag without value
                extra_args[key] = None
                i += 1
        else:
            # Skip non-flag arguments
            i += 1

    # Store the flags in app state for the lifespan to use
    app.state.cloudflare_tunnel = args.cloudflare_tunnel
    app.state.port = args.port
    app.state.extra_args = (
        extra_args  # Store ALL extra args to pass to HeadlessClaudeRunner
    )

    print("[INFO] Starting Claude Code Webhook Server")
    print(f"  - Host: {DEFAULT_HOST}")
    print(f"  - Port: {args.port}")
    if args.cloudflare_tunnel:
        print("  - Cloudflare tunnel: Enabled")
    if extra_args:
        print(f"  - Extra args for Claude: {extra_args}")
    print()

    uvicorn.run(app, host=DEFAULT_HOST, port=args.port)
