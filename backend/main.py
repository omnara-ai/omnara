"""FastAPI backend for Agent Dashboard"""

import logging
import os

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware
import sentry_sdk
from shared.config import settings
from .api import (
    agents,
    questions,
    user_agents,
    push_notifications,
    billing,
    mobile_billing,
    user_settings,
    cli_auth,
)
from .auth import routes as auth_routes

# Configure logging
logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

# Initialize Sentry only if DSN is provided
if settings.sentry_dsn:
    sentry_sdk.init(
        dsn=settings.sentry_dsn,
        send_default_pii=True,
        environment=settings.environment,
    )
    logger.info(f"Sentry initialized for {settings.environment} environment")
else:
    logger.info("Sentry DSN not provided, error tracking disabled")

# Create FastAPI app
app = FastAPI(
    title="Agent Dashboard API",
    description="Backend API for monitoring and interacting with AI agents",
    version="1.0.0",
)

# Configure CORS - cannot use wildcard (*) with credentials
# Define localhost origins for both development and production access
localhost_origins = [
    "http://localhost:3000",
    "http://127.0.0.1:3000",
    "http://localhost:5173",  # Vite default
    "http://127.0.0.1:5173",
    "http://localhost:8080",
    "http://127.0.0.1:8080",
    "http://localhost:8081",  # Custom frontend port
    "http://127.0.0.1:8081",
]

if os.getenv("ENVIRONMENT", "development") == "development":
    # In development, use localhost origins
    allowed_origins = localhost_origins
else:
    # Production origins from configuration
    allowed_origins = (
        settings.frontend_urls + localhost_origins
    )  # Include localhost URLs in production too

app.add_middleware(
    CORSMiddleware,
    allow_origins=allowed_origins,
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Include routers with versioned API prefix
app.include_router(auth_routes.router, prefix=settings.api_v1_prefix)
app.include_router(agents.router, prefix=settings.api_v1_prefix)
app.include_router(questions.router, prefix=settings.api_v1_prefix)
app.include_router(user_agents.router, prefix=settings.api_v1_prefix)
app.include_router(push_notifications.router, prefix=settings.api_v1_prefix)
app.include_router(user_settings.router, prefix=settings.api_v1_prefix)

# CLI auth router without API prefix for OAuth flow
app.include_router(cli_auth.router)

# Conditionally include billing router if Stripe is configured
if settings.stripe_secret_key:
    app.include_router(billing.router, prefix=settings.api_v1_prefix)
    logger.info("Billing endpoints enabled")
else:
    logger.info("Billing endpoints disabled - STRIPE_SECRET_KEY not configured")

# Always include mobile billing router (includes RevenueCat webhooks)
app.include_router(mobile_billing.router, prefix=settings.api_v1_prefix)
logger.info("Mobile billing and RevenueCat webhook endpoints enabled")


@app.get("/")
async def root():
    """Root endpoint"""
    return {"message": "Agent Dashboard API", "version": "1.0.0", "docs": "/docs"}


@app.get("/health")
async def health_check():
    """Health check endpoint"""
    return {"status": "healthy"}


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("backend.main:app", host="0.0.0.0", port=settings.api_port, reload=True)


def main():
    """Entry point for module execution"""
    import uvicorn

    uvicorn.run("backend.main:app", host="0.0.0.0", port=settings.api_port)


if __name__ == "__main__":
    main()
