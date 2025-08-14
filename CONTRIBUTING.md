# Contributing to Omnara

Thanks for your interest in contributing!

## Quick Start

1. Fork and clone the repository
2. Set up your development environment:
   ```bash
   python -m venv .venv
   source .venv/bin/activate  # Windows: .venv\Scripts\activate
   make dev-install
   make pre-commit-install
   ```
3. Set up PostgreSQL and configure `DATABASE_URL` in `.env`
4. Generate JWT keys: `python scripts/generate_jwt_keys.py`
5. Run migrations: `cd shared && alembic upgrade head`

## Development Process

1. Create a branch: `feature/`, `bugfix/`, or `docs/`
2. Make your changes
3. Run checks: `make lint` and `make test`
4. Submit a pull request

## Code Style

- Python 3.11+
- Type hints required
- Follow existing patterns
- Tests for new features

## Database Changes

When modifying models:

1. Edit models in `shared/database/models.py`
2. Generate migration: `cd shared && alembic revision --autogenerate -m "description"`
3. Test migration before committing

## Commit Messages

Use conventional commits:

- `feat:` New feature
- `fix:` Bug fix
- `docs:` Documentation
- `refactor:` Code refactoring
- `test:` Tests

Example: `feat: add API key rotation endpoint`

## Questions?

Open an issue or discussion on GitHub!