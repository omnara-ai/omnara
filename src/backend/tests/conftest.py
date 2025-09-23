"""Pytest configuration and fixtures for backend tests."""

import os
import pytest
from datetime import datetime, timezone
from uuid import uuid4
from unittest.mock import Mock

from fastapi.testclient import TestClient
from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker
from testcontainers.postgres import PostgresContainer

from shared.database.models import Base, User, UserAgent, AgentInstance
from shared.database.enums import AgentStatus
from backend.main import app
from backend.auth.dependencies import get_current_user, get_optional_current_user
from shared.database.session import get_db


@pytest.fixture(scope="session")
def postgres_container():
    """Create a PostgreSQL container for testing - shared across all tests."""
    with PostgresContainer("postgres:16-alpine") as postgres:
        yield postgres


@pytest.fixture
def test_db(postgres_container):
    """Create a test database session using PostgreSQL."""
    # Get connection URL from container
    db_url = postgres_container.get_connection_url()

    # Create engine and tables
    engine = create_engine(db_url)
    Base.metadata.create_all(bind=engine)

    # Create session
    TestSessionLocal = sessionmaker(autocommit=False, autoflush=False, bind=engine)
    session = TestSessionLocal()

    yield session

    session.close()
    Base.metadata.drop_all(bind=engine)
    engine.dispose()


@pytest.fixture
def test_user(test_db):
    """Create a test user."""
    user = User(
        id=uuid4(),
        email="test@example.com",
        display_name="Test User",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )
    test_db.add(user)
    test_db.commit()
    return user


@pytest.fixture
def test_user_agent(test_db, test_user):
    """Create a test user agent."""
    user_agent = UserAgent(
        id=uuid4(),
        user_id=test_user.id,
        name="claude code",  # Lowercase as per the actual implementation
        is_active=True,
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )
    test_db.add(user_agent)
    test_db.commit()
    return user_agent


@pytest.fixture
def test_agent_instance(test_db, test_user, test_user_agent):
    """Create a test agent instance."""
    instance = AgentInstance(
        id=uuid4(),
        user_agent_id=test_user_agent.id,
        user_id=test_user.id,
        status=AgentStatus.ACTIVE,
        started_at=datetime.now(timezone.utc),
    )
    test_db.add(instance)
    test_db.commit()
    return instance


@pytest.fixture
def client(test_db):
    """Create a test client with database override."""

    def override_get_db():
        try:
            yield test_db
        finally:
            pass

    app.dependency_overrides[get_db] = override_get_db

    with TestClient(app) as test_client:
        yield test_client

    app.dependency_overrides.clear()


@pytest.fixture
def authenticated_client(client, test_user):
    """Create a test client with authentication."""

    def override_get_current_user():
        return test_user

    def override_get_optional_current_user():
        return test_user

    app.dependency_overrides[get_current_user] = override_get_current_user
    app.dependency_overrides[get_optional_current_user] = (
        override_get_optional_current_user
    )

    yield client

    # Clear only the auth overrides, keep the db override
    if get_current_user in app.dependency_overrides:
        del app.dependency_overrides[get_current_user]
    if get_optional_current_user in app.dependency_overrides:
        del app.dependency_overrides[get_optional_current_user]


@pytest.fixture
def mock_supabase_client():
    """Mock Supabase client for auth tests."""
    mock = Mock()
    mock.auth = Mock()
    mock.auth.get_user = Mock()
    return mock


@pytest.fixture(autouse=True)
def reset_env():
    """Reset environment variables for each test."""
    original_env = os.environ.copy()
    yield
    os.environ.clear()
    os.environ.update(original_env)
