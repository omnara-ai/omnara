# Shared

This directory contains shared infrastructure for database operations and configurations used across the Omnara platform.

## Purpose

The shared directory serves as the single source of truth for:
- Database schema definitions and models
- Database connection management
- Configuration settings
- Schema migration infrastructure

## Architecture

### Database Layer
- **ORM**: SQLAlchemy 2.0+ with modern declarative mapping
- **Database**: PostgreSQL for reliable, scalable data persistence
- **Models**: Centralized schema definitions for all platform entities
- **Session Management**: Shared database connection handling

### Configuration Management
- Environment-aware settings (development, production)
- Centralized configuration using Pydantic settings
- Support for multiple deployment scenarios

### Schema Migrations
- Alembic for version-controlled database schema changes
- Automatic migration application during startup
- Safe rollback capabilities

## Database Migrations

### Essential Commands

```bash
# Apply pending migrations
cd shared/
alembic upgrade head

# Create a new migration after model changes
alembic revision --autogenerate -m "Description of changes"

# Check migration status
alembic current

# View migration history
alembic history

# Rollback one migration
alembic downgrade -1
```

### Migration Workflow

1. Modify database models
2. Generate migration: `alembic revision --autogenerate -m "Description"`
3. Review generated migration file
4. Apply migration (automatic on restart or manual with `alembic upgrade head`)
5. Commit both model changes and migration files

**Important**: Always create migrations when changing the database schema. A pre-commit hook enforces this requirement.

## Key Benefits

- **Consistency**: Single schema definition prevents drift between services
- **Type Safety**: Shared type definitions and enumerations
- **Maintainability**: Centralized database operations reduce duplication
- **Version Control**: Migration history tracks all schema changes
- **Multi-Service**: Both API backend and MCP servers use the same database layer

## Dependencies

Core dependencies are managed in `requirements.txt` and include:
- SQLAlchemy for ORM functionality
- PostgreSQL driver for database connectivity
- Pydantic for configuration and validation
- Alembic for migration management