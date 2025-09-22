#!/bin/bash
# Script to auto-format code

set -e

echo "Running ruff format..."
ruff format . --exclude src/integrations/cli_wrappers/codex

echo -e "\nRunning ruff check with auto-fix..."
ruff check --fix . --exclude src/integrations/cli_wrappers/codex

echo -e "\nFormatting complete!"
