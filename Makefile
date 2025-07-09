.PHONY: install lint format typecheck dev-install pre-commit-install pre-commit-run

# Install production dependencies
install:
	pip install -r backend/requirements.txt
	pip install -r servers/requirements.txt

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
	./scripts/lint.sh

# Auto-format code
format:
	./scripts/format.sh

# Run only type checking
typecheck:
	pyright

# Run only ruff linting
ruff-check:
	ruff check .

# Run only ruff formatting check
ruff-format-check:
	ruff format --check .

# Run tests
test:
	./scripts/run_all_tests.sh

# Run SDK tests
test-sdk:
	cd sdk/python && pytest tests -v

# Run backend tests
test-backend:
	cd backend && pytest tests -v || echo "No backend tests yet"

# Run server tests  
test-servers:
	cd servers && pytest tests -v || echo "No server tests yet"

# Run all tests with coverage
test-coverage:
	cd sdk/python && pytest tests --cov=omnara --cov-report=term-missing

# Run integration tests with PostgreSQL (requires Docker)
test-integration:
	pytest servers/tests/test_integration.py -v -m integration
