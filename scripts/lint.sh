#!/bin/bash
# Script to run linting and type checking

set -e

echo "Running ruff check..."
ruff check .

echo -e "\nRunning ruff format check..."
ruff format --check .

echo -e "\nRunning pyright..."
pyright

echo -e "\nAll checks passed!"