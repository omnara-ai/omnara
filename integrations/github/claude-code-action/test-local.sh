#!/bin/bash

# Local test script for Omnara integration

echo "🧪 Omnara Local Test"
echo "==================="

# Check prerequisites
command -v omnara >/dev/null 2>&1 || { echo "❌ Omnara CLI not found. Please install: pip install omnara"; exit 1; }
command -v claude >/dev/null 2>&1 || { echo "❌ Claude CLI not found. Please install from claude.ai"; exit 1; }
command -v bun >/dev/null 2>&1 || { echo "❌ Bun not found. Please install: curl -fsSL https://bun.sh/install | bash"; exit 1; }

# Set up test environment
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-sk-ant-test}"
export OMNARA_API_KEY="omni_test_local_key"
export OMNARA_AGENT_INSTANCE_ID="gh-local-test-$(date +%s)"
export OMNARA_AGENT_TYPE="test"
export RUNNER_TEMP="/tmp/omnara-test"

mkdir -p "$RUNNER_TEMP"

echo ""
echo "📝 Configuration:"
echo "  Agent ID: $OMNARA_AGENT_INSTANCE_ID"
echo "  Agent Type: $OMNARA_AGENT_TYPE"
echo "  Temp Dir: $RUNNER_TEMP"
echo ""

# Test 1: Direct Omnara CLI
echo "Test 1: Direct Omnara CLI"
echo "-------------------------"
echo "Testing: echo 'Hi' | omnara headless --name test -p"
echo "Hi from test" | omnara headless --name "$OMNARA_AGENT_TYPE" -p > "$RUNNER_TEMP/omnara-direct.json" 2>&1

if [ $? -eq 0 ]; then
    echo "✅ Direct Omnara CLI test passed"
else
    echo "❌ Direct Omnara CLI test failed"
    cat "$RUNNER_TEMP/omnara-direct.json"
fi

echo ""

# Test 2: Base Action
echo "Test 2: Base Action Integration"
echo "-------------------------------"
cd /Users/kartiksarangmath/Documents/omnara/claude-code-base-action

# Create prompt file
echo "Create a file called test.txt with 'Hello from Omnara'" > "$RUNNER_TEMP/prompt.txt"

export INPUT_PROMPT_FILE="$RUNNER_TEMP/prompt.txt"
export INPUT_ALLOWED_TOOLS="Write,Read"
export INPUT_MAX_TURNS="1"
export CLAUDE_CODE_ACTION="1"

echo "Running base action..."
timeout 30 bun run src/index.ts

if [ $? -eq 0 ]; then
    echo "✅ Base action test passed"
    # Check if output file was created
    if [ -f "$RUNNER_TEMP/omnara-execution-output.json" ]; then
        echo "📄 Output file created successfully"
        echo "First 500 chars of output:"
        head -c 500 "$RUNNER_TEMP/omnara-execution-output.json"
        echo ""
    fi
else
    echo "❌ Base action test failed"
fi

echo ""

# Test 3: Full Action (if act is installed)
if command -v act >/dev/null 2>&1; then
    echo "Test 3: Full GitHub Action (with act)"
    echo "-------------------------------------"
    cd /Users/kartiksarangmath/Documents/omnara/claude-code-action
    
    # Create event file
    cat > "$RUNNER_TEMP/test-event.json" << EOF
{
  "action": "repository_dispatch",
  "event_type": "omnara_trigger",
  "client_payload": {
    "agent_instance_id": "$OMNARA_AGENT_INSTANCE_ID",
    "prompt": "Create a hello.py file",
    "omnara_api_key": "$OMNARA_API_KEY",
    "branch_name": "test-local",
    "agent_type": "test"
  }
}
EOF
    
    # Create secrets file
    cat > "$RUNNER_TEMP/.env.secrets" << EOF
ANTHROPIC_API_KEY=$ANTHROPIC_API_KEY
EOF
    
    echo "Running with act..."
    act repository_dispatch -e "$RUNNER_TEMP/test-event.json" --secret-file "$RUNNER_TEMP/.env.secrets" -W .github/workflows/omnara-dispatch.yml --container-architecture linux/amd64
    
    if [ $? -eq 0 ]; then
        echo "✅ Full action test passed"
    else
        echo "⚠️ Full action test failed (this might be due to act limitations)"
    fi
else
    echo "ℹ️ Skipping full action test (act not installed)"
    echo "   Install act to test: brew install act"
fi

echo ""
echo "==================="
echo "🏁 Tests completed!"
echo ""
echo "📁 Test artifacts in: $RUNNER_TEMP"
echo ""
echo "Next steps:"
echo "1. If tests passed, push your changes to GitHub"
echo "2. Create the GitHub App using the manifest"
echo "3. Test with a real repository"