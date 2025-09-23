# Backend API

This directory contains the FastAPI backend that serves the web dashboard for monitoring and managing AI agent instances.

## Overview

The backend provides a REST API for accessing and managing agent-related data. Its primary purpose is to handle read operations for agent instances, their execution history, and user interactions. The API serves as the bridge between client applications and the underlying agent data stored in the database.

## Architecture

### Core Components

- **API Routes** - RESTful endpoints organized by resource type
- **Authentication** - Dual-layer authentication system for web users and agent clients
- **Database Layer** - Query interfaces for accessing agent and user data
- **Models** - Request/response schemas and data validation

### Directory Structure

- `api/` - API route handlers organized by domain
- `auth/` - Authentication and authorization logic
- `db/` - Database queries and data access layer
- `models.py` - Pydantic models for API contracts
- `main.py` - Application entry point and configuration
- `tests/` - Test suite for API functionality

## Key Features

- **Agent Monitoring** - View agent types, instances, and execution history
- **Unified Messaging** - All agent interactions (steps, questions, feedback) through a single messaging system
- **Multi-tenancy** - User-scoped data isolation and access control
- **Authentication** - Support for both web dashboard users and programmatic agent access
- **User Agent Management** - Custom agent configurations and webhook integrations

## Authentication

The backend implements a dual authentication system:

1. **Web Dashboard Authentication** - For users accessing the web interface
2. **API Key Authentication** - For programmatic access by agent clients

All data access is scoped to the authenticated user, ensuring proper data isolation in a multi-tenant environment.

### Security Notice: API Key Storage

**⚠️ Important**: API keys are currently stored in plain text in the database for development convenience. While this is more permissible for write-only keys, developers should:
- Be aware of this when handling API key data
- Never expose API keys in logs or error messages
- Treat API keys as sensitive data despite being write-only

**TODO**: Migrate to hashed storage (SHA256) in future releases. The plain text storage is temporary for easier development and debugging.

## Development

### Prerequisites

- Python 3.12+
- PostgreSQL database
- Required environment variables configured

### Setup

1. Install dependencies from `requirements.txt`
2. Configure environment variables
3. Set up the database schema
4. Run the development server

### Testing

The test suite covers API endpoints, authentication flows, and data access patterns. Tests use pytest and can be run from the backend directory.

## Configuration

The backend uses environment variables for configuration, including:

- Database connection settings
- Authentication providers
- CORS and security settings
- External service integrations

Refer to the project documentation for specific configuration requirements.