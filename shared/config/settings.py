import os
from typing import List

from pydantic import field_validator
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


class Settings(BaseSettings):
    # Environment Configuration
    environment: str = "development"
    development_db_url: str = (
        "postgresql://user:password@localhost:5432/agent_dashboard"
    )
    production_db_url: str = ""

    # Database URL - can be set directly or will use development/production URLs
    database_url: str = ""

    # MCP Server - use PORT env var if available (for Render), otherwise default
    mcp_server_port: int = get_port_from_env()

    # Backend API - use PORT env var if available (for Render), otherwise default
    api_port: int = int(os.getenv("PORT") or os.getenv("API_PORT") or "8000")

    @field_validator("database_url", mode="after")
    @classmethod
    def set_database_url(cls, v, info):
        """Set database URL based on environment if not explicitly provided."""
        if v:  # If explicitly set, use it
            return v

        # Use info.data to access other fields
        environment = info.data.get("environment", "development").lower()

        if environment == "production":
            production_url = info.data.get("production_db_url")
            if production_url:
                return production_url

        development_url = info.data.get("development_db_url")
        if development_url:
            return development_url

        return "postgresql://user:password@localhost:5432/agent_dashboard"

    # Frontend URLs - expects JSON array in env var
    frontend_urls: List[str] = ["http://localhost:3000"]

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

    # Billing Configuration
    enforce_limits: bool = False  # Default to unlimited access
    stripe_secret_key: str = ""
    stripe_webhook_secret: str = ""

    # Stripe Price IDs (from your Stripe dashboard)
    stripe_pro_price_id: str = ""
    stripe_enterprise_price_id: str = ""

    # Plan Configuration - used when enforce_limits is True
    free_plan_agent_limit: int = 20  # 20 total agents per month
    pro_plan_agent_limit: int = -1  # Unlimited
    pro_plan_price: float = 9
    enterprise_plan_agent_limit: int = -1  # Unlimited
    enterprise_plan_price: float = 500

    model_config = SettingsConfigDict(env_file=".env", env_file_encoding="utf-8")


settings = Settings()
