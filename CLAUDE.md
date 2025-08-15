# Claude Code Development Guide for Omnara

Welcome Claude! This document contains everything you need to know to work effectively on the Omnara project.

## Project Overview

Omnara is a platform that allows users to communicate with their AI agents (like you!) from anywhere. It uses the Model Context Protocol (MCP) to enable real-time communication between agents and users through a web dashboard.

## Quick Context

- **Purpose**: Let users see what their AI agents are doing and communicate with them in real-time
- **Key Innovation**: Agents can ask questions and receive feedback while working
- **Architecture**: Separate read (backend) and write (servers) operations for optimal performance
- **Open Source**: This is a community project - code quality and clarity matter!

## Project Structure

```
omnara/
├── backend/          # FastAPI - Web dashboard API (read operations)
├── servers/          # FastAPI + MCP - Agent communication server (write operations)
├── shared/           # Shared database models and infrastructure
├── omnara/           # Python package directory
│   └── sdk/          # Python SDK for agent integration
├── cli/              # Node.js CLI tool for MCP configuration
├── scripts/          # Utility scripts (JWT generation, linting, etc.)
├── tests/            # Integration tests
└── integrations/     # Integration handlers (webhooks, CLI wrappers, etc.)
```

## Key Technical Decisions

### Authentication Architecture
- **Two separate JWT systems**:
  1. **Backend**: Supabase JWTs for web users
  2. **Servers**: Custom JWT with weaker RSA (shorter API keys for agents)
- API keys are hashed (SHA256) before storage - never store raw tokens

### Database Design
- **PostgreSQL** with **SQLAlchemy 2.0+**
- **Alembic** for migrations - ALWAYS create migrations for schema changes
- Multi-tenant design - all data is scoped by user_id
- Key tables: users, user_agents, agent_instances, messages, api_keys
- **Unified messaging system**: All agent interactions (steps, questions, feedback) are now stored in the `messages` table with `sender_type` and `requires_user_input` fields

### Server Architecture
- **Unified server** (`servers/app.py`) supports both MCP and REST
- MCP endpoint: `/mcp/`
- REST endpoints: `/api/v1/*`
- Both use the same authentication and business logic

## Development Workflow

### Setting Up
1. **Always activate the virtual environment first**:
   ```bash
   source .venv/bin/activate  # macOS/Linux
   .venv\Scripts\activate     # Windows
   ```

2. **Install pre-commit hooks** (one-time):
   ```bash
   make pre-commit-install
   ```

### Before Making Changes
1. **Check current branch**: Ensure you're on the right branch
2. **Update dependencies**: Run `pip install -r requirements.txt` if needed
3. **Check migrations**: Run `alembic current` in `shared/` directory

### Making Changes

#### Database Changes
1. Modify models in `shared/models/`
2. Generate migration:
   ```bash
   cd shared/
   alembic revision --autogenerate -m "Descriptive message"
   ```
3. Review the generated migration file
4. Test migration: `alembic upgrade head`
5. Include migration file in your commit

#### Code Changes
1. **Follow existing patterns** - check similar files first
2. **Use type hints** - We use Python 3.12 with full type annotations
3. **Import style**: Prefer absolute imports from project root

#### Testing
```bash
make test              # Run all tests
make test-integration  # Integration tests (needs Docker)
```

### Before Committing
1. **Run linting and formatting**:
   ```bash
   make lint    # Check for issues
   make format  # Auto-fix formatting
   ```

2. **Verify your changes work**:
   - Test the specific functionality you changed
   - Run relevant test suites
   - Check that migrations apply cleanly

3. **Update documentation** if you changed functionality

## Common Tasks

### Working with Messages
The unified messaging system uses a single `messages` table:
- **Agent messages**: Set `sender_type=AGENT`, use `requires_user_input=True` for questions
- **User messages**: Set `sender_type=USER` for feedback/responses
- **Reading messages**: Use `last_read_message_id` to track reading progress
- **Queued messages**: Agent receives unread user messages when sending new messages

### Adding a New API Endpoint
1. Add route in `backend/api/` or `servers/fastapi_server/routers.py`
2. Create Pydantic models for request/response in `models.py`
3. Add database queries in appropriate query files
4. Write tests for the endpoint

### Adding a New MCP Tool
1. Add tool definition in `servers/mcp_server/tools.py`
2. Register tool in `servers/mcp_server/server.py`
3. Share logic with REST endpoint if applicable
4. Update agent documentation

### Modifying Database Schema
1. Change models in `shared/models/`
2. Generate and review migration
3. Update any affected queries
4. Update Pydantic models if needed
5. Test thoroughly with existing data

## Important Files to Know

- `shared/config.py` - Central configuration using Pydantic settings
- `shared/models/base.py` - SQLAlchemy base configuration
- `servers/app.py` - Unified server entry point
- `backend/auth/` - Authentication logic for web users
- `servers/fastapi_server/auth.py` - Agent authentication

## Environment Variables

Key variables you might need:
- `DATABASE_URL` - PostgreSQL connection
- `JWT_PUBLIC_KEY` / `JWT_PRIVATE_KEY` - For agent auth
- `SUPABASE_URL` / `SUPABASE_ANON_KEY` - For web auth
- `ENVIRONMENT` - Set to "development" for auto-reload

## Common Pitfalls to Avoid

1. **Don't commit without migrations** - Pre-commit hooks will catch this
2. **Don't store raw JWT tokens** - Always hash API keys
3. **Don't mix authentication systems** - Backend uses Supabase, Servers use custom JWT
4. **Don't forget user scoping** - All queries must filter by user_id
5. **Don't skip type hints** - Pyright will complain

## Debugging Tips

1. **Database issues**: Check migrations are up to date
2. **Auth failures**: Verify JWT keys are properly formatted (with newlines)
3. **Import errors**: Ensure you're using absolute imports
4. **Type errors**: Run `make typecheck` to catch issues early

## Getting Help

- Check existing code for patterns
- Read test files for usage examples
- Error messages usually indicate what's wrong
- The codebase is well-structured - similar things are grouped together

## Your Superpowers on This Project

As Claude Code, you're particularly good at:
- Understanding the full codebase quickly
- Maintaining consistency across files
- Catching potential security issues
- Writing comprehensive tests
- Suggesting architectural improvements

Remember: This is an open-source project that helps AI agents communicate with humans. Your work here directly improves the AI-human collaboration experience!