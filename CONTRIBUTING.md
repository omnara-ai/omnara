# Contributing to Omnara

Thanks for your interest in contributing!

## Prerequisites

- **Docker** (required for automated setup)
- **Python 3.11+**
- **Git**

Note: PostgreSQL runs in Docker, no local database installation needed!

## Quick Start (Automated)

The fastest way to get started:

1. **Fork and clone the repository**
   ```bash
   git clone https://github.com/omnara-ai/omnara.git
   cd omnara
   ```

2. **Copy environment configuration**
   ```bash
   cp .env.example .env
   ```

3. **Generate JWT keys**
   ```bash
   python infrastructure/scripts/generate_jwt_keys.py
   ```

4. **Start everything with one command**
   ```bash
   ./dev-start.sh
   ```
   This automatically:
   - Starts PostgreSQL in Docker
   - Runs database migrations
   - Starts the Backend API (port 8000)
   - Starts the Unified Server (port 8080)

5. **When you're done developing**
   ```bash
   ./dev-stop.sh
   ```

### Resetting the Database
If you need a fresh database:
```bash
./dev-start.sh --reset-db
```

## Alternative: Manual Setup

If you prefer manual control:

1. Set up your development environment:
   ```bash
   python -m venv .venv
   source .venv/bin/activate  # Windows: .venv\Scripts\activate
   make dev-install
   make pre-commit-install
   ```

2. Set up PostgreSQL and configure `DATABASE_URL` in `.env`

3. Generate JWT keys: `python infrastructure/scripts/generate_jwt_keys.py`

4. Run migrations: `cd src/shared && alembic upgrade head`

5. Start services manually:
   ```bash
   # Set Python path (required for imports)
   export PYTHONPATH="$(pwd)/src"

   # Terminal 1: Unified Server
   python -m servers.app

   # Terminal 2: Backend API (in project root, not backend/)
   uvicorn backend.main:app --port 8000
   ```

## Development Process

1. Create a branch: `feature/`, `bugfix/`, or `docs/`
2. Make your changes
3. Run checks: `make lint` and `make test`
4. Submit a pull request

## Code Style

- Python 3.10+
- Type hints required
- Follow existing patterns
- Tests for new features

## Database Changes

When modifying models:

1. Edit models in `src/shared/database/models.py`
2. Generate migration: `cd src/shared && alembic revision --autogenerate -m "description"`
3. Test migration before committing

## Commit Messages

Use conventional commits:

- `feat:` New feature
- `fix:` Bug fix
- `docs:` Documentation
- `refactor:` Code refactoring
- `test:` Tests

Example: `feat: add API key rotation endpoint`

## Available Commands

```bash
make lint          # Run linting checks
make format        # Auto-format code
make test          # Run all tests
make test-unit     # Unit tests only
make test-integration  # Integration tests (needs Docker)
make typecheck     # Type checking
```

## Questions?

Open an issue on GitHub!
