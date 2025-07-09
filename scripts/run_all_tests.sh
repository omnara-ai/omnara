#!/bin/bash
# Run all Python tests

set -e

echo "🧪 Running Python Tests"
echo "======================"

# Change to root directory
cd "$(dirname "$0")/.."

# Run pytest - configuration is in pytest.ini and pyproject.toml
pytest "$@"

echo -e "\n✅ Tests completed!"