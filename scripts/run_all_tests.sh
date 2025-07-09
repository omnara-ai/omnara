#!/bin/bash
# Run all Python tests across the monorepo

set -e

# Set test environment to disable Sentry
export ENVIRONMENT=test
export SENTRY_DSN=""

echo "🧪 Running All Python Tests"
echo "==========================="

# Store the root directory (parent of scripts dir)
ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Function to run tests in a directory if they exist
run_component_tests() {
    local component=$1
    local test_dir="$ROOT_DIR/$component"
    
    if [ -d "$test_dir" ]; then
        echo -e "\n📦 Testing $component..."
        cd "$ROOT_DIR"  # Stay in root directory
        
        # Run pytest if tests directory exists
        if [ -d "$test_dir/tests" ]; then
            PYTHONPATH="$ROOT_DIR:$PYTHONPATH" pytest "$component/tests" -v
        else
            echo "  No tests found in $component"
        fi
    fi
}

# Run tests for each component
run_component_tests "backend"
run_component_tests "servers"

# Run root-level integration tests if they exist
if [ -d "$ROOT_DIR/tests" ]; then
    echo -e "\n🌐 Running integration tests..."
    cd "$ROOT_DIR"
    pytest tests -v
fi

echo -e "\n✅ All tests completed!"