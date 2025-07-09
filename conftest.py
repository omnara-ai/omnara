"""Root-level pytest configuration and shared fixtures."""

import os
import pytest

# Set test environment
os.environ["ENVIRONMENT"] = "test"
os.environ["SENTRY_DSN"] = ""


@pytest.fixture(scope="session", autouse=True)
def test_environment():
    """Ensure test environment is set."""
    original_env = os.environ.copy()
    
    # Set test environment variables
    os.environ["ENVIRONMENT"] = "test"
    os.environ["SENTRY_DSN"] = ""
    
    yield
    
    # Restore original environment
    os.environ.clear()
    os.environ.update(original_env)