# Omnara Platform Integration Guide

## How to Trigger GitHub Workflows from Omnara Platform

### API Endpoint
```
POST https://api.github.com/repos/{owner}/{repo}/dispatches
```

### Required Headers
```http
Authorization: Bearer {INSTALLATION_ACCESS_TOKEN}
Accept: application/vnd.github.v3+json
Content-Type: application/json
X-GitHub-Api-Version: 2022-11-28
```

### Request Body
```json
{
  "event_type": "omnara_trigger",
  "client_payload": {
    "agent_instance_id": "gh-owner-repo-issue-123-1234567890",
    "prompt": "Fix the bug in authentication logic",
    "omnara_api_key": "omni_key_xxxxx",
    "branch_name": "fix/auth-bug",
    "agent_type": "debugging"
  }
}
```

### Parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `agent_instance_id` | Yes | Unique identifier for this Omnara session |
| `prompt` | Yes | The initial prompt to pass to Claude |
| `omnara_api_key` | Yes | API key for Omnara platform callbacks |
| `branch_name` | No | Git branch to work on (defaults to main) |
| `agent_type` | No | Type of agent for `--name` parameter (defaults to "general") |

### Example Python Code

```python
import requests
from datetime import datetime

def trigger_omnara_workflow(
    owner: str,
    repo: str,
    installation_token: str,
    agent_instance_id: str,
    prompt: str,
    omnara_api_key: str,
    branch_name: str = None,
    agent_type: str = "general"
):
    """
    Trigger Omnara workflow in a GitHub repository
    """
    url = f"https://api.github.com/repos/{owner}/{repo}/dispatches"
    
    headers = {
        "Authorization": f"Bearer {installation_token}",
        "Accept": "application/vnd.github.v3+json",
        "Content-Type": "application/json",
        "X-GitHub-Api-Version": "2022-11-28"
    }
    
    payload = {
        "event_type": "omnara_trigger",
        "client_payload": {
            "agent_instance_id": agent_instance_id,
            "prompt": prompt,
            "omnara_api_key": omnara_api_key,
            "branch_name": branch_name or "main",
            "agent_type": agent_type
        }
    }
    
    response = requests.post(url, json=payload, headers=headers)
    
    if response.status_code == 204:
        return {"success": True, "message": "Workflow triggered successfully"}
    else:
        return {
            "success": False, 
            "error": f"Failed with status {response.status_code}: {response.text}"
        }

# Example usage
result = trigger_omnara_workflow(
    owner="user",
    repo="my-project",
    installation_token="ghs_xxxxxxxxxxxx",  # GitHub App installation token
    agent_instance_id=f"gh-user-project-task-{datetime.now().timestamp()}",
    prompt="Add error handling to the login function",
    omnara_api_key="omni_key_xxxxx",
    branch_name="feature/error-handling",
    agent_type="enhancement"
)
```

### Example cURL Command

```bash
curl -X POST \
  -H "Authorization: Bearer ghs_xxxxxxxxxxxx" \
  -H "Accept: application/vnd.github.v3+json" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  https://api.github.com/repos/owner/repo/dispatches \
  -d '{
    "event_type": "omnara_trigger",
    "client_payload": {
      "agent_instance_id": "gh-owner-repo-task-1234567890",
      "prompt": "Fix the authentication bug",
      "omnara_api_key": "omni_key_xxxxx",
      "branch_name": "fix/auth",
      "agent_type": "debugging"
    }
  }'
```

### Getting the Installation Access Token

Users need to generate an installation access token for their GitHub App installation. This can be done:

1. **Via GitHub API** (requires App private key):
```python
import jwt
import time
import requests

def get_installation_token(app_id, private_key, installation_id):
    # Create JWT
    now = int(time.time())
    payload = {
        "iat": now - 60,
        "exp": now + (10 * 60),
        "iss": app_id
    }
    jwt_token = jwt.encode(payload, private_key, algorithm="RS256")
    
    # Get installation token
    headers = {
        "Authorization": f"Bearer {jwt_token}",
        "Accept": "application/vnd.github.v3+json"
    }
    
    response = requests.post(
        f"https://api.github.com/app/installations/{installation_id}/access_tokens",
        headers=headers
    )
    
    return response.json()["token"]
```

2. **Via GitHub UI** (easier for users):
   - Go to Settings → Developer settings → GitHub Apps
   - Click on "Omnara Code Assistant"
   - Generate a new installation token

### Response

GitHub returns a `204 No Content` status on success. The workflow will start running asynchronously.

### Checking Workflow Status

To check if the workflow is running:

```python
def check_workflow_runs(owner, repo, installation_token):
    url = f"https://api.github.com/repos/{owner}/{repo}/actions/runs"
    headers = {
        "Authorization": f"Bearer {installation_token}",
        "Accept": "application/vnd.github.v3+json"
    }
    
    response = requests.get(url, headers=headers)
    runs = response.json()["workflow_runs"]
    
    # Find runs triggered by repository_dispatch
    omnara_runs = [
        run for run in runs 
        if run["event"] == "repository_dispatch"
    ]
    
    return omnara_runs
```