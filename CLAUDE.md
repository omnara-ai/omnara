# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Omnara is a platform that enables real-time monitoring and interaction with AI agents (Claude Code, Codex CLI, n8n workflows, etc.) through mobile, web, and API interfaces. Users can see what their agents are doing and respond to questions instantly.

**Key Innovation**: Bidirectional communication - agents send progress updates and questions, users respond from any device.

## Architecture

### Monorepo Structure
```
src/
├── omnara/         # Python CLI & SDK (pip install omnara)
├── backend/        # FastAPI web dashboard API (READ operations)
├── servers/        # Unified agent server (WRITE operations)
│   ├── mcp/       # Model Context Protocol interface
│   ├── api/       # REST API interface
│   └── shared/    # Common business logic
├── shared/        # Database models, migrations, config
├── relay_server/  # WebSocket relay for terminal streaming
├── integrations/  # Agent wrappers, webhooks, n8n nodes
└── mcp-installer/ # NPX tool for MCP config

apps/
├── web/           # Next.js dashboard
└── mobile/        # React Native app

infrastructure/    # Docker, deployment, scripts
```

### Critical Architectural Decisions

**1. Dual Server Architecture**
- **Backend** (`src/backend/`): Web dashboard API - READ operations only
  - Auth: Supabase JWTs for web users
  - Port: 8000

- **Servers** (`src/servers/`): Agent communication - WRITE operations only
  - Auth: Custom JWT with weaker RSA (shorter API keys)
  - Port: 8080
  - Exposes both MCP (`/mcp/`) and REST (`/api/v1/`) interfaces

**2. Unified Messaging System**
All agent interactions flow through the `messages` table:
- `sender_type`: AGENT or USER
- `requires_user_input`: Boolean flag for questions vs. updates
- `last_read_message_id`: Track reading progress per instance
- Agents receive queued user messages when sending new messages

**3. Multi-Protocol Support**
The unified server (`src/servers/app.py`) supports:
- **MCP Protocol**: For MCP-compatible agents (Claude Code, etc.)
- **REST API**: For SDK clients and direct integrations
- Both share identical authentication and business logic

## Development Commands

### Setup
```bash
# First time setup
cp .env.example .env
python infrastructure/scripts/generate_jwt_keys.py
./dev-start.sh  # Starts PostgreSQL (Docker) + all servers

# Stop everything
./dev-stop.sh

# Reset database
./dev-start.sh --reset-db
```

### Daily Development
```bash
make lint          # Run all checks (ruff + pyright)
make format        # Auto-format with ruff
make typecheck     # Type checking only
make test          # All tests
make test-unit     # Skip integration tests
make test-integration  # Docker-dependent tests only

# Run specific test
make test-k ARGS="test_auth"
```

### Database Migrations

**CRITICAL**: Always run from `src/shared/` directory:

```bash
cd src/shared/

# Check current migration status
alembic current

# Create migration after model changes
alembic revision --autogenerate -m "description"

# Apply migrations
alembic upgrade head

# Rollback one migration
alembic downgrade -1
```

**Pre-commit hook enforces**: Model changes must have corresponding migrations.

## Key Technical Constraints

### Authentication Security
- **Backend**: Uses Supabase JWTs - DO NOT mix with server auth
- **Servers**: Custom JWT with weaker RSA implementation
  - ⚠️ Keep BOTH private AND public keys secure
  - API keys are hashed (SHA256) before storage - never store raw tokens

### Database Rules
1. All models in `src/shared/database/models.py`
2. Multi-tenant: ALL queries must filter by `user_id`
3. Use SQLAlchemy 2.0+ async patterns
4. Migrations are version-controlled - commit them with model changes

### Import Patterns
```python
# Always use absolute imports from project root
from shared.database.models import User, AgentInstance
from servers.shared.messages import create_message
from backend.auth.supabase import get_current_user

# Set PYTHONPATH when running manually
export PYTHONPATH="$(pwd)/src"
```

### Running Services Manually
```bash
# From project root with PYTHONPATH set
export PYTHONPATH="$(pwd)/src"

# Backend (port 8000)
uvicorn backend.main:app --port 8000

# Unified Server (port 8080)
python -m servers.app

# Relay Server (port 8787)
python -m relay_server.app
```

## Testing Philosophy

- Python 3.10+ required (3.11+ preferred)
- Use pytest with async support (`asyncio_mode = "auto"`)
- Mark integration tests: `@pytest.mark.integration`
- Integration tests need Docker (PostgreSQL)
- Pre-commit hooks run ruff formatting and migration checks

## Common Workflows

### Adding an API Endpoint

**Backend (read operations)**:
1. Add route in `src/backend/api/`
2. Create Pydantic models in `src/backend/models.py`
3. Add query in `src/backend/db/`
4. Write tests in `src/backend/tests/`

**Servers (write operations)**:
1. Add to both `src/servers/mcp/tools.py` AND `src/servers/api/routers.py`
2. Share logic via `src/servers/shared/`
3. Test both MCP and REST interfaces

### Modifying Database Schema
1. Edit `src/shared/database/models.py`
2. `cd src/shared/ && alembic revision --autogenerate -m "description"`
3. Review generated migration (edit if needed)
4. Test: `alembic upgrade head`
5. Update any affected Pydantic schemas
6. Commit both model and migration files

### Working with Messages
```python
# Agent sends a question
create_message(
    agent_instance_id=instance_id,
    sender_type=SenderType.AGENT,
    content="Should I refactor this module?",
    requires_user_input=True
)

# Agent sends progress update
create_message(
    agent_instance_id=instance_id,
    sender_type=SenderType.AGENT,
    content="Analyzing codebase structure",
    requires_user_input=False
)

# User responds
create_message(
    agent_instance_id=instance_id,
    sender_type=SenderType.USER,
    content="Yes, refactor it"
)
```

## Environment Variables

Core variables (see `.env.example` for complete list):
- `DATABASE_URL`: PostgreSQL connection
- `DEVELOPMENT_DB_URL`: Override for local dev
- `JWT_PUBLIC_KEY` / `JWT_PRIVATE_KEY`: Agent auth (RSA keys with newlines)
- `SUPABASE_URL` / `SUPABASE_ANON_KEY`: Web auth
- `ENVIRONMENT`: Set to "development" for auto-reload

## Commit Conventions

Follow conventional commits:
- `feat:` New features
- `fix:` Bug fixes
- `docs:` Documentation only
- `refactor:` Code restructuring
- `test:` Test additions/changes

Example: `feat: add message filtering by date range`

## Type Hints & Code Style

- Python 3.10+ with full type annotations required
- Ruff for linting and formatting (replaces black/flake8)
- Pyright for type checking
- Prefer `Mapped[type]` for SQLAlchemy columns
- Use Pydantic v2 for validation schemas

## Pitfalls to Avoid

1. **Don't run migrations from wrong directory** - Always `cd src/shared/` first
2. **Don't skip migrations** - Pre-commit hook will block commits
3. **Don't mix auth systems** - Backend ≠ Servers authentication
4. **Don't forget user scoping** - All queries need `user_id` filter
5. **Don't store raw API keys** - Hash with SHA256 first
6. **Don't expose JWT keys** - Both public and private keys are sensitive

## Deployment

Uses automated scripts:
- `./dev-start.sh`: Local development (PostgreSQL in Docker)
- `./dev-stop.sh`: Stop all services
- See `infrastructure/` for production deployment configs

## Package Distribution

Omnara is published to PyPI as `omnara`:
- Version defined in `pyproject.toml`
- CLI entry point: `omnara.cli:main`
- Includes: CLI, SDK, MCP server, agent wrappers
- Install: `pip install omnara` or `uv tool install omnara`

## Docs

### Getting Started
- [README](README.md): Project overview, quick start, and installation guide
- [CONTRIBUTING](CONTRIBUTING.md): Guide for contributing to the Omnara project
- [AGENTS](AGENTS.md): Claude Code development guide for working on Omnara

### CLI & Usage
- [CLI Reference](docs/cli-reference.md): Complete command-line interface documentation with all commands, options, and examples

### Architecture & Integration
- [Architecture Diagram](docs/guides/architecture-diagram.md): System architecture overview with Mermaid diagrams showing component interactions and data flow
- [n8n Integration](docs/n8n.md): Comprehensive n8n workflow integration architecture, webhook configuration, and AI agent tool setup

### Deployment
- [Fly.io Setup Guide](docs/deployment/fly-io-setup.md): Step-by-step guide for deploying Omnara to Fly.io with Supabase authentication
- [Deployment Quick Start](DEPLOYMENT_QUICK_START.md): Rapid deployment instructions
- [Deployment Summary](DEPLOYMENT_SUMMARY.md): Overview of deployment architecture and decisions
