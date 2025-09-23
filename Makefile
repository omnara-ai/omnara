.PHONY: install lint format typecheck dev-install pre-commit-install pre-commit-run

# Install production dependencies
install:
	pip install -r src/backend/requirements.txt
	pip install -r src/servers/requirements.txt

# Install development dependencies
dev-install: install
	pip install -r requirements-dev.txt

# Install pre-commit hooks
pre-commit-install: dev-install
	pre-commit install

# Run pre-commit on all files
pre-commit-run:
	pre-commit run --all-files

# Run all linting and type checking
lint:
	./infrastructure/scripts/lint.sh

# Auto-format code
format:
	./infrastructure/scripts/format.sh

# Run only type checking
typecheck:
	pyright

# Run only ruff linting
ruff-check:
	ruff check .

# Run only ruff formatting check
ruff-format-check:
	ruff format --check .

# Run all tests
test:
	python -m pytest

# Run unit tests only (skip integration)
test-unit:
	python -m pytest -m "not integration"

# Run integration tests only
test-integration:
	python -m pytest -m integration

# Run tests with coverage
test-coverage:
	python -m pytest --cov=src/backend --cov=src/servers --cov=src/omnara --cov=src/shared --cov-report=term-missing

# Run specific test file or pattern
# Usage: make test-k ARGS="test_auth"
test-k:
	python -m pytest -k "$(ARGS)"
