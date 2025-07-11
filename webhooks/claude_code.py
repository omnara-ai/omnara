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
Use the Omnara MCP server to log steps and ask questions.
Ensure this is the only way you communicate with the user from now on,
until the task is complete. Start by asking the user what they want to do, through Omnara.
If a session has already started, continue to communicate through Omnara.

The following agent-instance-id must be used in Omnara: {{agent_instance_id}}
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
