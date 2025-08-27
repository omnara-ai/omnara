#!/usr/bin/env python3
"""
GitHub Webhook Server for Omnara Integration

This server receives GitHub webhooks and triggers repository_dispatch events
to run the Omnara GitHub Action.
"""

import os
import json
import hmac
import hashlib
import logging
from typing import Dict, Any, Optional
from datetime import datetime

import requests
from fastapi import FastAPI, Request, HTTPException, Header
from pydantic import BaseModel

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

app = FastAPI(title="Omnara GitHub Webhook Server")

# Configuration
GITHUB_WEBHOOK_SECRET = os.environ.get("GITHUB_WEBHOOK_SECRET", "")
GITHUB_PAT = os.environ.get("GITHUB_PAT", "")
OMNARA_API_KEY = os.environ.get("OMNARA_API_KEY", "")
OMNARA_API_URL = os.environ.get("OMNARA_API_URL", "https://api.omnara.ai")


class TriggerRequest(BaseModel):
    """Request model for manual trigger endpoint"""

    owner: str
    repo: str
    prompt: str
    agent_instance_id: Optional[str] = None
    agent_type: Optional[str] = "Claude Code"
    branch: Optional[str] = None


def verify_webhook_signature(payload_body: bytes, signature_header: str) -> bool:
    """Verify GitHub webhook signature"""
    if not GITHUB_WEBHOOK_SECRET:
        return True  # Skip verification if no secret configured

    expected_signature = (
        "sha256="
        + hmac.new(
            GITHUB_WEBHOOK_SECRET.encode(), payload_body, hashlib.sha256
        ).hexdigest()
    )

    return hmac.compare_digest(expected_signature, signature_header)


def should_trigger_omnara(
    event_type: str, payload: Dict[str, Any]
) -> tuple[bool, Optional[str]]:
    """
    Determine if we should trigger Omnara based on the event
    Returns: (should_trigger, prompt)
    """
    if event_type == "issue_comment":
        comment_body = payload.get("comment", {}).get("body", "")
        if "@omnara" in comment_body.lower():
            # Extract prompt after @omnara mention
            prompt = comment_body.split("@omnara", 1)[1].strip()
            return True, prompt

    elif event_type == "issues":
        action = payload.get("action")
        if action in ["opened", "edited"]:
            issue_body = payload.get("issue", {}).get("body", "")
            if "@omnara" in issue_body.lower():
                prompt = issue_body.split("@omnara", 1)[1].strip()
                return True, prompt

    elif event_type == "pull_request":
        action = payload.get("action")
        if action in ["opened", "edited"]:
            pr_body = payload.get("pull_request", {}).get("body", "")
            if "@omnara" in pr_body.lower():
                prompt = pr_body.split("@omnara", 1)[1].strip()
                return True, prompt

    elif event_type == "pull_request_review_comment":
        comment_body = payload.get("comment", {}).get("body", "")
        if "@omnara" in comment_body.lower():
            prompt = comment_body.split("@omnara", 1)[1].strip()
            return True, prompt

    return False, None


def trigger_repository_dispatch(
    owner: str,
    repo: str,
    prompt: str,
    agent_instance_id: Optional[str] = None,
    agent_type: str = "Claude Code",
    context: Optional[Dict[str, Any]] = None,
) -> bool:
    """Trigger a repository_dispatch event via GitHub API"""

    if not GITHUB_PAT:
        logger.error("No GitHub PAT configured")
        return False

    url = f"https://api.github.com/repos/{owner}/{repo}/dispatches"

    # Generate instance ID if not provided
    if not agent_instance_id:
        agent_instance_id = f"omnara-{datetime.now().strftime('%Y%m%d-%H%M%S')}"

    payload = {
        "event_type": "omnara-trigger",
        "client_payload": {
            "prompt": prompt,
            "agent_instance_id": agent_instance_id,
            "agent_type": agent_type,
            "omnara_api_key": OMNARA_API_KEY,
            "triggered_by": "webhook",
            "timestamp": datetime.now().isoformat(),
        },
    }

    # Add context if provided
    if context:
        payload["client_payload"]["context"] = context

    headers = {
        "Accept": "application/vnd.github.v3+json",
        "Authorization": f"Bearer {GITHUB_PAT}",
        "X-GitHub-Api-Version": "2022-11-28",
    }

    try:
        response = requests.post(url, json=payload, headers=headers)
        response.raise_for_status()
        logger.info(f"Successfully triggered repository_dispatch for {owner}/{repo}")
        return True
    except requests.exceptions.RequestException as e:
        logger.error(f"Failed to trigger repository_dispatch: {e}")
        if hasattr(e, "response") and e.response is not None:
            logger.error(f"Response: {e.response.text}")
        return False


@app.post("/github/webhooks")
async def github_webhook(
    request: Request,
    x_hub_signature_256: Optional[str] = Header(None),
    x_github_event: str = Header(None),
    x_github_delivery: str = Header(None),
):
    """Handle GitHub webhook events"""

    # Get raw payload
    payload_body = await request.body()

    # Verify signature
    if x_hub_signature_256 and not verify_webhook_signature(
        payload_body, x_hub_signature_256
    ):
        raise HTTPException(status_code=401, detail="Invalid signature")

    # Parse payload
    try:
        payload = json.loads(payload_body)
    except json.JSONDecodeError:
        raise HTTPException(status_code=400, detail="Invalid JSON payload")

    logger.info(f"Received {x_github_event} event (delivery: {x_github_delivery})")

    # Check if we should trigger Omnara
    should_trigger, prompt = should_trigger_omnara(x_github_event, payload)

    if not should_trigger or not prompt:
        return {"status": "ignored", "reason": "No Omnara trigger found"}

    # Extract repository info
    repository = payload.get("repository", {})
    owner = repository.get("owner", {}).get("login")
    repo = repository.get("name")

    if not owner or not repo:
        raise HTTPException(status_code=400, detail="Missing repository information")

    # Build context
    context = {
        "event_type": x_github_event,
        "delivery_id": x_github_delivery,
    }

    # Add relevant IDs based on event type
    if "issue" in payload:
        context["issue_number"] = payload["issue"]["number"]
    if "pull_request" in payload:
        context["pr_number"] = payload["pull_request"]["number"]
    if "comment" in payload:
        context["comment_id"] = payload["comment"]["id"]

    # Trigger repository dispatch
    success = trigger_repository_dispatch(
        owner=owner, repo=repo, prompt=prompt, agent_type="Claude Code", context=context
    )

    if success:
        return {
            "status": "triggered",
            "repository": f"{owner}/{repo}",
            "prompt": prompt[:100] + "..." if prompt and len(prompt) > 100 else prompt,
        }
    else:
        raise HTTPException(status_code=500, detail="Failed to trigger workflow")


@app.post("/trigger")
async def manual_trigger(request: TriggerRequest):
    """Manually trigger Omnara on a repository"""

    success = trigger_repository_dispatch(
        owner=request.owner,
        repo=request.repo,
        prompt=request.prompt,
        agent_instance_id=request.agent_instance_id,
        agent_type=request.agent_type or "Claude Code",
    )

    if success:
        return {
            "status": "triggered",
            "repository": f"{request.owner}/{request.repo}",
            "prompt": request.prompt,
        }
    else:
        raise HTTPException(status_code=500, detail="Failed to trigger workflow")


@app.get("/health")
async def health_check():
    """Health check endpoint"""
    return {
        "status": "healthy",
        "configured": {
            "webhook_secret": bool(GITHUB_WEBHOOK_SECRET),
            "github_pat": bool(GITHUB_PAT),
            "omnara_api_key": bool(OMNARA_API_KEY),
        },
    }


@app.get("/")
async def root():
    """Root endpoint with API information"""
    return {
        "service": "Omnara GitHub Webhook Server",
        "version": "1.0.0",
        "endpoints": {
            "/health": "Health check",
            "/github/webhooks": "GitHub webhook receiver",
            "/trigger": "Manual trigger endpoint",
        },
    }


if __name__ == "__main__":
    import uvicorn

    port = int(os.environ.get("PORT", 8000))
    host = os.environ.get("HOST", "0.0.0.0")

    logger.info(f"Starting webhook server on {host}:{port}")
    uvicorn.run(app, host=host, port=port)
