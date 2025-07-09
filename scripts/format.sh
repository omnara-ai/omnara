#!/bin/bash
# Script to auto-format code

set -e

echo "Running ruff format..."
ruff format .

echo -e "\nRunning ruff check with auto-fix..."
ruff check --fix .

echo -e "\nFormatting complete!"