# infrastructure/

This directory contains DevOps, deployment, and infrastructure-related files.

## Structure

- **`docker/`** - Docker configuration files
  - `backend.Dockerfile` - Docker image for web API backend
  - `servers.Dockerfile` - Docker image for MCP/REST servers
  - `db-init.Dockerfile` - Database initialization container

- **`scripts/`** - Build and utility scripts
  - `format.sh` - Code formatting with Ruff
  - `lint.sh` - Code linting checks
  - `generate_jwt_keys.py` - JWT key generation for API auth
  - `init-db.sh` - Database initialization script
  - `run_all_tests.sh` - Test runner script

This directory consolidates all infrastructure and DevOps tooling in one location, making deployment and local development setup clearer.