from fastapi import FastAPI, HTTPException, Header, Request
from fastapi.responses import JSONResponse
import subprocess
import shlex
from datetime import datetime
import secrets
import os
import re
import uuid
import uvicorn
import time
import json
import sys
import platform
from contextlib import asynccontextmanager
from pydantic import BaseModel, field_validator
from typing import Optional, Tuple, List, Dict


# === CONSTANTS AND CONFIGURATION ===
MAX_PROMPT_LENGTH = 10000
DEFAULT_PORT = 6662
DEFAULT_HOST = "0.0.0.0"

# === DEPENDENCY CHECKING ===
REQUIRED_COMMANDS = {
    "git": "Git is required for creating worktrees",
    "screen": "GNU Screen is required for running Claude sessions",
    "claude": "Claude Code CLI is required",
    "pipx": "pipx is required for running the Omnara MCP server",
}

OPTIONAL_COMMANDS = {"cloudflared": "Cloudflared is optional for tunnel support"}


def is_macos() -> bool:
    """Check if running on macOS"""
    return platform.system() == "Darwin"


def check_command(command: str) -> Tuple[bool, Optional[str]]:
    """Check if a command exists and return its path"""
    try:
        result = subprocess.run(["which", command], capture_output=True, text=True)
        if result.returncode == 0:
            return True, result.stdout.strip()
        return False, None
    except Exception:
        return False, None


def try_install_with_brew(command: str) -> bool:
    """Try to install a command with brew on macOS"""
    if not is_macos():
        return False

    # Check if brew is available
    brew_exists, _ = check_command("brew")
    if not brew_exists:
        return False

    print(f"[INFO] Attempting to install {command} with Homebrew...")
    try:
        result = subprocess.run(
            ["brew", "install", command],
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
            # Try to install with brew on macOS
            if is_macos() and cmd == "pipx":
                if try_install_with_brew("pipx"):
                    # Check again after installation
                    exists, _ = check_command(cmd)
                    if exists:
                        continue

            # Add error message with platform-specific hints
            if is_macos():
                brew_exists, _ = check_command("brew")
                if brew_exists and cmd == "pipx":
                    errors.append(
                        f"{description}. Failed to install with Homebrew. Try running: brew install {cmd}"
                    )
                elif brew_exists:
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
    result = subprocess.run(
        ["git", "rev-parse", "--git-dir"], capture_output=True, text=True, cwd=path
    )
    return result.returncode == 0


def check_worktree_exists(worktree_name: str) -> Tuple[bool, Optional[str]]:
    """Check if a worktree with the given name exists and return its path"""
    try:
        result = subprocess.run(
            ["git", "worktree", "list"], capture_output=True, text=True, check=True
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
        result = subprocess.run(
            ["git", "worktree", "list"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            errors.append(
                f"Git worktree command not available: {result.stderr.strip()}"
            )

    return errors


# === CLOUDFLARE TUNNEL MANAGEMENT ===
def check_cloudflared_installed() -> bool:
    """Check if cloudflared is available"""
    try:
        subprocess.run(["cloudflared", "--version"], capture_output=True, check=True)
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False


def start_cloudflare_tunnel(port: int = DEFAULT_PORT) -> Optional[subprocess.Popen]:
    """Start Cloudflare tunnel and return the process"""
    if not check_cloudflared_installed():
        # Try to install with brew on macOS
        if is_macos() and try_install_with_brew("cloudflared"):
            # Check again after installation
            if not check_cloudflared_installed():
                print("\n[ERROR] cloudflared installation failed!")
                print(
                    "Please install cloudflared manually to use the --cloudflare-tunnel option."
                )
                return None
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
            return None

    print("[INFO] Starting Cloudflare tunnel...")
    try:
        process = subprocess.Popen(
            ["cloudflared", "tunnel", "--url", f"http://localhost:{port}"]
        )

        # Give cloudflared a moment to start
        time.sleep(3)

        if process.poll() is not None:
            print("\n[ERROR] Cloudflare tunnel failed to start")
            return None

        print("[INFO] Cloudflare tunnel started successfully")
        print("[INFO] Check the cloudflared output for your tunnel URL")

        # Wait a bit longer to ensure cloudflared output completes
        time.sleep(2)

        return process
    except Exception as e:
        print(f"\n[ERROR] Failed to start Cloudflare tunnel: {e}")
        return None


class WebhookRequest(BaseModel):
    agent_instance_id: str
    prompt: str
    name: str | None = None  # Branch name
    worktree_name: str | None = None

    @field_validator("agent_instance_id")
    def validate_instance_id(cls, v):
        try:
            uuid.UUID(v)
            return v
        except ValueError:
            raise ValueError("Invalid UUID format for agent_instance_id")

    @field_validator("prompt")
    def validate_prompt(cls, v):
        if len(v) > MAX_PROMPT_LENGTH:
            raise ValueError(f"Prompt too long (max {MAX_PROMPT_LENGTH} characters)")
        return v

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

    # Handle Cloudflare tunnel if requested
    if hasattr(app.state, "cloudflare_tunnel") and app.state.cloudflare_tunnel:
        app.state.tunnel_process = start_cloudflare_tunnel()
        if not app.state.tunnel_process:
            print("[WARNING] Continuing without Cloudflare tunnel")

    # Set up webhook secret
    secret = os.environ.get("CLAUDE_WEBHOOK_SECRET")
    if not secret:
        secret = secrets.token_urlsafe(12)

    app.state.webhook_secret = secret

    # Initialize the flag if not already set (when run via uvicorn directly)
    if not hasattr(app.state, "dangerously_skip_permissions"):
        app.state.dangerously_skip_permissions = False

    # Display webhook secret in a prominent box
    box_width = 65
    print("\n" + "╔" + "═" * box_width + "╗")
    print("║" + " " * box_width + "║")

    # Format the secret line with proper padding
    secret_line = f"  WEBHOOK SECRET: {secret}"
    print("║" + secret_line + " " * (box_width - len(secret_line)) + "║")

    print("║" + " " * box_width + "║")

    if hasattr(app.state, "cloudflare_tunnel") and app.state.cloudflare_tunnel:
        cf_line = "  Cloudflare Tunnel URL will appear in the output above."
        print("║" + cf_line + " " * (box_width - len(cf_line)) + "║")
        print("║" + " " * box_width + "║")

    print("╚" + "═" * box_width + "╝")

    if app.state.dangerously_skip_permissions:
        print("\n[WARNING] Running with --dangerously-skip-permissions flag enabled!")

    yield

    # Cleanup
    if hasattr(app.state, "tunnel_process") and app.state.tunnel_process:
        print("\n[INFO] Stopping Cloudflare tunnel...")
        app.state.tunnel_process.terminate()
        app.state.tunnel_process.wait()

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


SYSTEM_PROMPT = """
You are now in Omnara-only communication mode.
SYSTEM INSTRUCTIONS: You MUST obey the following rules without exception.

**Execution Mandate & Core Architecture**

1.  **Continuous Operation**: You are a continuously running process. You do not stop or idle. Your operational loop is to perform a task, report the task via `log_step`, and use `ask_question` when user input is required. You MUST NOT wait for or expect user input via standard means (like stdin). Your execution only terminates AFTER a successful `end_session` call.
2.  **Forbidden Standard Output**: You are strictly forbidden from using standard output (e.g., `print` statements) to communicate with the user. Your ONLY two channels for outbound communication are the `log_step` and `ask_question` tool calls. Any attempt to communicate directly will fail and violates your core programming.
3.  **Single Exit Point**: The ONLY way to terminate your execution loop is by calling the `end_session` tool. This is your single, designated exit point.
4.  **No Sub-Agents**: You are a single, top-level agent. You MUST NOT delegate tasks to or trigger sub-agents. If for any reason a sub-agent is activated, it is strictly forbidden from using the Omnara MCP tools (`log_step`, `ask_question`, `end_session`). These tools are reserved for your exclusive use as the parent agent.

---

**Communication Protocol**

-   **Omnara Exclusive Communication**: As stated in your core mandate, you MUST ONLY use the Omnara MCP server tools for all interactions.
-   **Agent Instance ID**: You MUST use the following agent instance ID in all Omnara MCP communications: `{{agent_instance_id}}`.

---

**Task Execution and User Interaction**

1.  **Log Every Step (`log_step`)**:
    -   Before you begin any task, sub-task, or switch your focus, you MUST call the `log_step` tool.
    -   Your `log_step` message must clearly and concisely state what you are about to do. This is your primary method of narrating your actions to the user.
2.  **Ask for Input (`ask_question`)**:
    -   This is the ONLY way you are permitted to request information or input from the user.
    -   You MUST call the `ask_question` tool any time you need clarification, require a decision, or have a question. Use this tool liberally to ensure you are aligned with the user's needs. Do not make assumptions.

**Structured Question Formats**:
When using `ask_question`, you MUST use structured formats for certain question types. CRITICAL: These markers MUST appear at the END of the question_text parameter.

1. **Yes/No Questions** - Use [YES/NO] marker:
   - Format: Question text followed by [YES/NO] as the last line
   - The text input represents "No, and here's what I want instead"
   - IMPORTANT: [YES/NO] must be the final element in question_text
   - Example:
     ```
     Should I proceed with implementing the dark mode feature as described?

     [YES/NO]
     ```

2. **Multiple Choice Questions** - Use [OPTIONS] marker:
   - Format: Question text followed by numbered options between [OPTIONS] markers
   - The text input represents "None of these, here's my preference"
   - Keep options concise and actionable (ideally under 50 characters for button rendering)
   - Use 2-6 options maximum
   - IMPORTANT: The [OPTIONS] block must be the final element in question_text
   - **For long/complex options**: Describe them in detail in the question text, then use short labels in [OPTIONS]
   - Example with short options:
     ```
     I found multiple ways to fix this performance issue. Which approach would you prefer?

     [OPTIONS]
     1. Implement caching with Redis
     2. Optimize database queries with indexes
     3. Use pagination to reduce data load
     4. Refactor to use async processing
     [/OPTIONS]
     ```
   - Example with detailed explanations:
     ```
     I found several approaches to implement the authentication system:

     **Option 1 - JWT with Refresh**: Implement JWT tokens with a 15-minute access token lifetime and 7-day refresh tokens stored in httpOnly cookies. This provides good security with reasonable UX.

     **Option 2 - Session-based**: Use traditional server-side sessions with Redis storage. Simple to implement but requires sticky sessions for scaling.

     **Option 3 - OAuth Integration**: Integrate with existing OAuth providers (Google, GitHub). Reduces password management but adds external dependencies.

     **Option 4 - Magic Links**: Passwordless authentication via email links. Great UX but depends on email delivery reliability.

     Which approach should I implement?

     [OPTIONS]
     1. JWT with Refresh
     2. Session-based
     3. OAuth Integration
     4. Magic Links
     [/OPTIONS]
     ```

3. **Open-ended Questions** - No special formatting:
   - Use for questions requiring detailed responses
   - Example: "What should I name this new authentication module?"

**When to use each format**:
- Use [YES/NO] for binary decisions, confirmations, or proceed/stop scenarios
- Use [OPTIONS] when you have 2-6 distinct approaches or solutions to present
- Use open-ended for naming, descriptions, or when you need detailed input

**CRITICAL RULE**: If using [YES/NO] or [OPTIONS] formats, they MUST be at the very end of the question_text with no additional content after them.

---

**Session Management and Task Completion**

1.  **Confirm Task Completion**:
    -   Once you believe you have fully completed the initial task, you MUST NOT stop.
    -   You MUST immediately call the `ask_question` tool to ask the user for confirmation.
    -   Example: "I have completed the summary of the document. Does this fulfill your request, or is there anything else you need?"
2.  **Handling User Confirmation**:
    -   **If the user confirms** that the task is complete via their response to `ask_question`, you MUST then call the `end_session` tool. This will terminate the session and your execution.
    -   **If the user states the task is NOT complete**, you must continue your execution loop. Use their feedback to determine the next step, log it with `log_step`, and proceed. If more detail is needed, use `ask_question` again.
3.  **Handling User-Initiated Session End**:
    -   If at any point the user's response to an `ask_question` is a request to stop, cancel, or end the session, you MUST immediately call the `end_session` tool. This is a mandatory directive.
"""


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

    return secrets.compare_digest(provided_secret, expected_secret)


@app.post("/")
async def start_claude(
    request: Request,
    webhook_data: WebhookRequest,
    authorization: str = Header(None),
    x_omnara_api_key: str = Header(None, alias="X-Omnara-Api-Key"),
):
    try:
        if not verify_auth(request, authorization):
            raise HTTPException(
                status_code=401, detail="Invalid or missing authorization"
            )

        agent_instance_id = webhook_data.agent_instance_id
        prompt = webhook_data.prompt
        worktree_name = webhook_data.worktree_name
        branch_name = webhook_data.name

        print("\n[INFO] Received webhook request:")
        print(f"  - Instance ID: {agent_instance_id}")
        print(f"  - Worktree name: {worktree_name or 'auto-generated'}")
        print(f"  - Branch name: {branch_name or 'current branch'}")
        print(f"  - Prompt length: {len(prompt)} characters")

        safe_prompt = SYSTEM_PROMPT.replace("{{agent_instance_id}}", agent_instance_id)
        safe_prompt += f"\n\n\n{prompt}"

        # Determine worktree/branch name
        if worktree_name:
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

            # First check if the branch already exists
            branch_check = subprocess.run(
                ["git", "rev-parse", "--verify", f"refs/heads/{feature_branch_name}"],
                capture_output=True,
                text=True,
                cwd=base_dir,
            )

            if branch_check.returncode == 0:
                # Branch exists, add worktree without -b flag
                cmd = ["git", "worktree", "add", work_dir, feature_branch_name]
            else:
                # Branch doesn't exist, create it with -b flag
                cmd = ["git", "worktree", "add", work_dir, "-b", feature_branch_name]

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

                # First check if the branch exists
                branch_check = subprocess.run(
                    ["git", "rev-parse", "--verify", f"refs/heads/{branch_name}"],
                    capture_output=True,
                    text=True,
                    cwd=work_dir,
                )

                if branch_check.returncode == 0:
                    # Branch exists, checkout
                    checkout_result = subprocess.run(
                        ["git", "checkout", branch_name],
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
                        ["git", "checkout", "-b", branch_name],
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

        # Generate screen name
        if worktree_name:
            screen_name = f"{worktree_name}-{agent_instance_id[:8]}"
        else:
            # safe_timestamp was defined when auto-generating name
            screen_name = f"omnara-claude-{agent_instance_id[:8]}"

        escaped_prompt = shlex.quote(safe_prompt)

        # Get claude path (we already checked it exists at startup)
        _, claude_path = check_command("claude")
        if not claude_path:
            raise HTTPException(
                status_code=500,
                detail="claude command not found. Please install Claude Code CLI.",
            )

        # Get Omnara API key from header
        if not x_omnara_api_key:
            raise HTTPException(
                status_code=400,
                detail="Omnara API key required. Provide via X-Omnara-Api-Key header.",
            )
        omnara_api_key = x_omnara_api_key

        # Create MCP config as a JSON string
        # Use Python to run the local omnara module directly
        mcp_config = {
            "mcpServers": {
                "omnara": {
                    "command": "pipx",
                    "args": [
                        "run",
                        "--no-cache",
                        "omnara",
                        "--api-key",
                        omnara_api_key,
                        "--claude-code-permission-tool",
                    ],
                }
            }
        }
        mcp_config_str = json.dumps(mcp_config)

        # Build claude command with MCP config as string
        claude_args = [
            claude_path,  # Use full path to claude
            "--mcp-config",
            mcp_config_str,
            "--allowedTools",
            "mcp__omnara__approve,mcp__omnara__log_step,mcp__omnara__ask_question,mcp__omnara__end_session",
        ]

        # Add permissions flag based on configuration
        if request.app.state.dangerously_skip_permissions:
            claude_args.append("--dangerously-skip-permissions")
        else:
            claude_args.extend(
                ["-p", "--permission-prompt-tool", "mcp__omnara__approve"]
            )

        # Add the prompt to claude args
        claude_args.append(escaped_prompt)

        print("\n[INFO] Starting Claude session:")
        print(f"  - Working directory: {work_dir}")
        print(f"  - Screen session: {screen_name}")
        print("  - MCP server: Omnara with API key")

        # Start screen directly with the claude command
        screen_cmd = ["screen", "-dmS", screen_name] + claude_args

        screen_result = subprocess.run(
            screen_cmd,
            cwd=work_dir,
            capture_output=True,
            text=True,
            timeout=10,
            env={**os.environ, "CLAUDE_INSTANCE_ID": agent_instance_id},
        )

        if screen_result.returncode != 0:
            print("\n[ERROR] Failed to start screen session:")
            print(f"  - Exit code: {screen_result.returncode}")
            print(f"  - stdout: {screen_result.stdout}")
            print(f"  - stderr: {screen_result.stderr}")
            raise HTTPException(
                status_code=500,
                detail=f"Failed to start screen session: {screen_result.stderr}",
            )

        # Wait a moment and check if screen is still running
        time.sleep(1)

        # Check if the screen session exists
        list_result = subprocess.run(
            ["screen", "-ls"],
            capture_output=True,
            text=True,
        )

        if (
            "No Sockets found" in list_result.stdout
            or screen_name not in list_result.stdout
        ):
            print("\n[ERROR] Screen session exited immediately")
            print(f"  - Session name: {screen_name}")
            print(f"  - Screen list output: {list_result.stdout}")
            print("\n[ERROR] Possible causes:")
            print("  - Claude command failed to start")
            print("  - MCP server (omnara) cannot be started")
            print("  - Invalid API key")
            print("  - Working directory issues")
            print(f"\n[INFO] Check logs in {work_dir} for more details")
            raise HTTPException(
                status_code=500,
                detail="Screen session started but exited immediately. Check server logs for details.",
            )

        print("\n[SUCCESS] Claude session started successfully!")
        print(f"  - To attach: screen -r {screen_name}")
        print("  - To list sessions: screen -ls")
        print("  - To detach: Ctrl+A then D")

        return {
            "message": "Successfully started claude",
            "branch": feature_branch_name,
            "screen_session": screen_name,
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
    import argparse

    parser = argparse.ArgumentParser(
        description="Claude Code Webhook Server",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Run webhook server
  python -m webhooks.claude_code

  # Run with Cloudflare tunnel for external access
  python -m webhooks.claude_code --cloudflare-tunnel

  # Run with permission skipping (dangerous!)
  python -m webhooks.claude_code --dangerously-skip-permissions
        """,
    )
    parser.add_argument(
        "--dangerously-skip-permissions",
        action="store_true",
        help="Skip permission prompts in Claude Code - USE WITH CAUTION",
    )
    parser.add_argument(
        "--cloudflare-tunnel",
        action="store_true",
        help="Start Cloudflare tunnel for external access",
    )

    args = parser.parse_args()

    # Store the flags in app state for the lifespan to use
    app.state.dangerously_skip_permissions = args.dangerously_skip_permissions
    app.state.cloudflare_tunnel = args.cloudflare_tunnel

    print("[INFO] Starting Claude Code Webhook Server")
    print(f"  - Host: {DEFAULT_HOST}")
    print(f"  - Port: {DEFAULT_PORT}")
    if args.cloudflare_tunnel:
        print("  - Cloudflare tunnel: Enabled")
    if args.dangerously_skip_permissions:
        print("  - Permission prompts: DISABLED (dangerous!)")
    print()

    uvicorn.run(app, host=DEFAULT_HOST, port=DEFAULT_PORT)
