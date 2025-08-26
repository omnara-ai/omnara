#!/bin/bash

# Test script for Omnara GitHub integration
# This simulates what the Omnara platform will do

# Configuration - REPLACE THESE WITH YOUR VALUES
OWNER="your-github-username"
REPO="your-test-repo"
INSTALLATION_TOKEN="ghs_your_installation_token_here"
OMNARA_API_KEY="omni_key_test_12345"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "🚀 Omnara GitHub Integration Test"
echo "=================================="
echo ""

# Check if required values are set
if [[ "$OWNER" == "your-github-username" ]]; then
    echo -e "${RED}❌ Error: Please update the OWNER variable with your GitHub username${NC}"
    exit 1
fi

if [[ "$INSTALLATION_TOKEN" == "ghs_your_installation_token_here" ]]; then
    echo -e "${RED}❌ Error: Please update the INSTALLATION_TOKEN variable${NC}"
    echo ""
    echo "To get your installation token:"
    echo "1. Go to https://github.com/settings/apps"
    echo "2. Click on 'Omnara Code Assistant'"
    echo "3. Generate an installation access token"
    exit 1
fi

# Generate unique agent instance ID
TIMESTAMP=$(date +%s)
AGENT_INSTANCE_ID="gh-${OWNER}-${REPO}-test-${TIMESTAMP}"

echo -e "${YELLOW}Repository:${NC} ${OWNER}/${REPO}"
echo -e "${YELLOW}Agent ID:${NC} ${AGENT_INSTANCE_ID}"
echo ""

# Create the JSON payload
PAYLOAD=$(cat <<EOF
{
  "event_type": "omnara_trigger",
  "client_payload": {
    "agent_instance_id": "${AGENT_INSTANCE_ID}",
    "prompt": "Create a simple hello_world.py file that prints 'Hello from Omnara!'",
    "omnara_api_key": "${OMNARA_API_KEY}",
    "branch_name": "test/omnara-${TIMESTAMP}",
    "agent_type": "test"
  }
}
EOF
)

echo "📤 Sending repository_dispatch event to GitHub..."
echo ""

# Make the API call
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
  -H "Authorization: Bearer ${INSTALLATION_TOKEN}" \
  -H "Accept: application/vnd.github.v3+json" \
  -H "Content-Type: application/json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  "https://api.github.com/repos/${OWNER}/${REPO}/dispatches" \
  -d "${PAYLOAD}")

# Extract status code (last line)
STATUS_CODE=$(echo "$RESPONSE" | tail -n 1)
# Extract body (all but last line)
BODY=$(echo "$RESPONSE" | sed '$d')

if [[ "$STATUS_CODE" == "204" ]]; then
    echo -e "${GREEN}✅ Success! Workflow triggered successfully${NC}"
    echo ""
    echo "📋 Next steps:"
    echo "1. Check your repository's Actions tab: https://github.com/${OWNER}/${REPO}/actions"
    echo "2. Look for a workflow run with 'repository_dispatch' event"
    echo "3. The workflow should create a new branch: test/omnara-${TIMESTAMP}"
    echo "4. Check for the hello_world.py file in that branch"
    echo ""
    echo "🔗 Direct link to Actions: https://github.com/${OWNER}/${REPO}/actions"
elif [[ "$STATUS_CODE" == "404" ]]; then
    echo -e "${RED}❌ Error 404: Repository not found or app not installed${NC}"
    echo ""
    echo "Please check:"
    echo "1. The repository exists: https://github.com/${OWNER}/${REPO}"
    echo "2. The Omnara GitHub App is installed on this repository"
    echo "3. The installation token is valid"
elif [[ "$STATUS_CODE" == "401" ]]; then
    echo -e "${RED}❌ Error 401: Authentication failed${NC}"
    echo ""
    echo "Your installation token may be expired or invalid."
    echo "Please generate a new token from the GitHub App settings."
elif [[ "$STATUS_CODE" == "422" ]]; then
    echo -e "${RED}❌ Error 422: Invalid request${NC}"
    echo "Response: $BODY"
    echo ""
    echo "This usually means the workflow file is missing or incorrectly named."
    echo "Make sure .github/workflows/omnara.yml exists in your repository."
else
    echo -e "${RED}❌ Error: Unexpected status code: ${STATUS_CODE}${NC}"
    echo "Response: $BODY"
fi

echo ""
echo "=================================="
echo "Test completed at $(date)"