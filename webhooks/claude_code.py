from fastapi import FastAPI, HTTPException, Header, Request
import subprocess
import shlex
from datetime import datetime
import secrets
import os
import re
import uuid
import uvicorn
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

    print(f"[IMPORTANT] Webhook secret: {secret}")
    print("[IMPORTANT] Use this secret in the Authorization header as: Bearer <secret>")
    yield

    if hasattr(app.state, "webhook_secret"):
        delattr(app.state, "webhook_secret")


app = FastAPI(lifespan=lifespan)

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
    request: Request, webhook_data: WebhookRequest, authorization: str = Header(None)
):
    if not verify_auth(request, authorization):
        raise HTTPException(status_code=401, detail="Invalid or missing authorization")

    agent_instance_id = webhook_data.agent_instance_id
    prompt = webhook_data.prompt
    name = webhook_data.name

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

    try:
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
        claude_cmd = f"claude --dangerously-skip-permissions {escaped_prompt}"

        screen_result = subprocess.run(
            ["screen", "-dmS", screen_name, "bash", "-c", claude_cmd],
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

        print(f"[INFO] Started screen session: {screen_name}")
        print(f"[INFO] To attach: screen -r {screen_name}")

        return {
            "message": "Successfully started claude",
            "branch": feature_branch_name,
            "screen_session": screen_name,
        }

    except subprocess.TimeoutExpired:
        raise HTTPException(status_code=500, detail="Git operation timed out")
    except Exception as e:
        raise HTTPException(status_code=500, detail=f"Failed to start claude: {str(e)}")


@app.get("/health")
async def health_check():
    """Health check endpoint - no auth required"""
    return {"status": "healthy"}


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=6662)
