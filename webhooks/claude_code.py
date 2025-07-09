from fastapi import FastAPI, Request
import subprocess
from datetime import datetime
import jwt
import secrets
import string
import os
from datetime import timezone, timedelta
from contextlib import asynccontextmanager


@asynccontextmanager
async def lifespan(app: FastAPI):
    token = create_bearer_token()
    print(f"[remote_cc] Bearer token: {token}")
    yield


app = FastAPI(lifespan=lifespan)

SYSTEM_PROMPT = """
Use the Omnara MCP server to log steps and ask questions. 
Ensure this is the only way you communicate with the user from now on, 
until the task is complete. Start by asking the user what they want to do, through Omnara. 
If a session has already started, continue to communicate through Omnara.

The following agent-instance-id must be used in Omnara: {{agent_instance_id}}
"""


def generate_secret():
    if "CLAUDE_SECRET" not in os.environ:
        alphabet = string.ascii_letters + string.digits
        random_alphanumeric = "".join(secrets.choice(alphabet) for _ in range(16))
        os.environ["CLAUDE_SECRET"] = random_alphanumeric

    return os.environ["CLAUDE_SECRET"]


def create_bearer_token():
    secret = generate_secret()
    return jwt.encode(
        {"secret": secret, "exp": datetime.now(timezone.utc) + timedelta(hours=24)},
        secret,
        algorithm="HS256",
    )


def verify_bearer_token(token: str):
    try:
        jwt.decode(token, os.environ["CLAUDE_SECRET"], algorithms=["HS256"])
        return True
    except jwt.ExpiredSignatureError:
        return False


@app.post("/")
async def start_claude(request: Request):
    data = await request.json()
    agent_instance_id = data.get("agent_instance_id")
    prompt = (
        SYSTEM_PROMPT.replace("{{agent_instance_id}}", str(agent_instance_id))
        + f"\n\n\n{data.get('prompt')}"
    )

    now = datetime.now()
    timestamp_str = now.strftime("%Y%m%d%H%M%S")
    feature_branch_name = f"claude-feature-{timestamp_str}"

    subprocess.run(
        [
            "git",
            "worktree",
            "add",
            f"./{feature_branch_name}",
            "-b",
            feature_branch_name,
        ]
    )
    subprocess.Popen(
        ["claude", "--dangerously-skip-permissions", prompt],
        cwd=f"./{feature_branch_name}",
    )

    return {"message": "Successfully started claude!"}
