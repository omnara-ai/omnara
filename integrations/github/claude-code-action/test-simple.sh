#!/bin/bash

# Simple test for Omnara execution

echo "🧪 Testing Omnara Execution"
echo "=========================="

# Check if omnara is installed
command -v omnara >/dev/null 2>&1 || { echo "❌ Omnara CLI not found. Please install: pip install omnara"; exit 1; }

# Set up environment
export ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-your-api-key-here}"
export OMNARA_API_KEY="omni_test_local_key"
export OMNARA_AGENT_INSTANCE_ID="test-$(date +%s)"
export OMNARA_AGENT_TYPE="test"

echo ""
echo "📝 Configuration:"
echo "  Agent ID: $OMNARA_AGENT_INSTANCE_ID"
echo "  Agent Type: $OMNARA_AGENT_TYPE"
echo ""

# Test: Direct Omnara CLI
echo "Running: echo 'Create a file called hello.txt with Hello World' | omnara headless --name test -p"
echo ""
echo "Create a file called hello.txt with 'Hello World'" | omnara headless --name "$OMNARA_AGENT_TYPE" -p

if [ $? -eq 0 ]; then
    echo ""
    echo "✅ Omnara execution successful!"
    
    # Check if file was created
    if [ -f "hello.txt" ]; then
        echo "📄 File created successfully:"
        cat hello.txt
    fi
else
    echo "❌ Omnara execution failed"
fi