import os
import json

from pydantic_settings import BaseSettings, SettingsConfigDict


def get_port_from_env() -> int:
    """Get port from environment variables, handling potential string literals"""
    port_env = os.getenv("PORT")
    mcp_port_env = os.getenv("MCP_SERVER_PORT")

    # Handle case where PORT might be '$PORT' literal string
    if port_env and port_env != "$PORT":
        try:
            return int(port_env)
        except ValueError:
            pass

    if mcp_port_env and mcp_port_env != "$MCP_SERVER_PORT":
        try:
            return int(mcp_port_env)
        except ValueError:
            pass

    return 8080


def get_database_url() -> str:
    """Get database URL based on environment"""
    # Note: This will be called during Settings initialization,
    # so we need to read directly from environment variables
    environment = os.getenv("ENVIRONMENT", "development").lower()

    if environment == "production":
        production_url = os.getenv("PRODUCTION_DB_URL")
        if production_url:
            return production_url

    # Default to development URL or fallback
    development_url = os.getenv("DEVELOPMENT_DB_URL")
    if development_url:
        return development_url

    # Final fallback to local PostgreSQL
    return "postgresql://user:password@localhost:5432/agent_dashboard"


class Settings(BaseSettings):
    # Environment Configuration
    environment: str = "development"
    development_db_url: str = (
        "postgresql://user:password@localhost:5432/agent_dashboard"
    )
    production_db_url: str = ""

    # Database - automatically chooses based on ENVIRONMENT variable
    database_url: str = get_database_url()

    # MCP Server - use PORT env var if available (for Render), otherwise default
    mcp_server_port: int = get_port_from_env()

    # Backend API - use PORT env var if available (for Render), otherwise default
    api_port: int = int(os.getenv("PORT") or os.getenv("API_PORT") or "8000")

    # Frontend URLs - can be set as JSON array string in env var
    @property
    def frontend_urls(self) -> list[str]:
        frontend_urls_env = os.getenv("FRONTEND_URLS")
        if frontend_urls_env:
            try:
                return json.loads(frontend_urls_env)
            except json.JSONDecodeError:
                # If it's a single URL string, wrap it in a list
                return [frontend_urls_env]
        return ["http://localhost:3000"]

    # API Versioning
    api_v1_prefix: str = "/api/v1"

    # Supabase Configuration
    supabase_url: str = ""
    supabase_anon_key: str = ""
    supabase_service_role_key: str = ""

    # JWT Signing Keys for API Keys
    jwt_private_key: str = ""
    jwt_public_key: str = ""

    # Sentry Configuration
    sentry_dsn: str = ""

    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")


settings = Settings()
