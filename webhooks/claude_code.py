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
from contextlib import asynccontextmanager
from pydantic import BaseModel, field_validator


MAX_PROMPT_LENGTH = 10000


class WebhookRequest(BaseModel):
    agent_instance_id: str
    prompt: str
    name: str | None = None

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
                raise ValueError("Name must contain only letters, numbers, and hyphens")
            if len(v) > 50:
                raise ValueError("Name must be 50 characters or less")
        return v


@asynccontextmanager
async def lifespan(app: FastAPI):
    secret = os.environ.get("CLAUDE_WEBHOOK_SECRET")
    if not secret:
        secret = secrets.token_urlsafe(12)

    app.state.webhook_secret = secret

    # Initialize the flag if not already set (when run via uvicorn directly)
    if not hasattr(app.state, "dangerously_skip_permissions"):
        app.state.dangerously_skip_permissions = False

    print(f"[IMPORTANT] Webhook secret: {secret}")
    print("[IMPORTANT] Use this secret in the Authorization header as: Bearer <secret>")
    if app.state.dangerously_skip_permissions:
        print("[WARNING] Running with --dangerously-skip-permissions flag enabled!")
    yield

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
        name = webhook_data.name

        print(
            f"[INFO] Received webhook request for agent instance: {agent_instance_id}"
        )

        safe_prompt = SYSTEM_PROMPT.replace("{{agent_instance_id}}", agent_instance_id)
        safe_prompt += f"\n\n\n{prompt}"

        now = datetime.now()
        timestamp_str = now.strftime("%Y%m%d%H%M%S")

        safe_timestamp = re.sub(r"[^a-zA-Z0-9-]", "", timestamp_str)

        prefix = name if name else "omnara-claude"
        feature_branch_name = f"{prefix}-{safe_timestamp}"

        work_dir = os.path.abspath(f"./{feature_branch_name}")
        base_dir = os.path.abspath(".")

        if not work_dir.startswith(base_dir):
            raise HTTPException(status_code=400, detail="Invalid working directory")
        result = subprocess.run(
            [
                "git",
                "worktree",
                "add",
                work_dir,
                "-b",
                feature_branch_name,
            ],
            capture_output=True,
            text=True,
            timeout=30,
            cwd=base_dir,
        )

        if result.returncode != 0:
            raise HTTPException(
                status_code=500, detail=f"Failed to create worktree: {result.stderr}"
            )

        screen_prefix = name if name else "omnara-claude"
        screen_name = f"{screen_prefix}-{safe_timestamp}"

        escaped_prompt = shlex.quote(safe_prompt)

        # First, check if required commands are available
        screen_check = subprocess.run(
            ["which", "screen"],
            capture_output=True,
            text=True,
        )
        if screen_check.returncode != 0:
            raise HTTPException(
                status_code=500,
                detail="screen command not found. Please install screen.",
            )

        # Check if claude command is available
        claude_check = subprocess.run(
            ["which", "claude"],
            capture_output=True,
            text=True,
        )
        if claude_check.returncode != 0:
            raise HTTPException(
                status_code=500,
                detail="claude command not found. Please install Claude Code CLI.",
            )
        claude_path = claude_check.stdout.strip()

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

        print(f"[INFO] Claude command: {' '.join(claude_args)}")
        print(f"[INFO] Working directory: {work_dir}")

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
            raise HTTPException(
                status_code=500,
                detail=f"Screen session started but exited immediately. Session name: {screen_name}",
            )

        print(f"[INFO] Started screen session: {screen_name}")
        print(f"[INFO] To attach: screen -r {screen_name}")

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

    parser = argparse.ArgumentParser(description="Claude Code Webhook Server")
    parser.add_argument(
        "--dangerously-skip-permissions",
        action="store_true",
        help="Skip permission prompts in Claude Code - USE WITH CAUTION",
    )

    args = parser.parse_args()

    # Store the flag in a global variable for the app to use
    app.state.dangerously_skip_permissions = args.dangerously_skip_permissions

    uvicorn.run(app, host="0.0.0.0", port=6662)
