"""Pytest configuration and fixtures for servers tests."""

import os
import pytest
import pytest_asyncio
from unittest.mock import Mock, AsyncMock
from datetime import datetime, timezone
from uuid import uuid4

from sqlalchemy import create_engine
from sqlalchemy.orm import sessionmaker
from testcontainers.postgres import PostgresContainer

from shared.database.models import Base, User, UserAgent, AgentInstance
from shared.database.enums import AgentStatus


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

    # Create test data
    test_user = User(
        id=uuid4(),
        email="test@example.com",
        display_name="Test User",
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )

    test_user_agent = UserAgent(
        id=uuid4(),
        user_id=test_user.id,
        name="Claude Code",
        is_active=True,
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )

    session.add(test_user)
    session.add(test_user_agent)
    session.commit()

    yield session

    session.close()
    Base.metadata.drop_all(bind=engine)
    engine.dispose()


@pytest.fixture
def mock_jwt_payload():
    """Mock JWT payload for authentication tests."""
    return {
        "sub": str(uuid4()),
        "email": "test@example.com",
        "iat": datetime.now(timezone.utc).timestamp(),
        "exp": (datetime.now(timezone.utc).timestamp() + 3600),
    }


@pytest.fixture
def mock_context(test_db, mock_jwt_payload):
    """Mock MCP context with authentication."""
    context = Mock()
    context.user_id = mock_jwt_payload["sub"]
    context.user_email = mock_jwt_payload["email"]
    context.db = test_db
    return context


@pytest_asyncio.fixture
async def async_mock_context(test_db, mock_jwt_payload):
    """Async mock MCP context for async tests."""
    context = AsyncMock()
    context.user_id = mock_jwt_payload["sub"]
    context.user_email = mock_jwt_payload["email"]
    context.db = test_db
    return context


@pytest.fixture
def test_agent_instance(test_db):
    """Create a test agent instance."""
    # Get test user and user agent
    user = test_db.query(User).first()
    user_agent = test_db.query(UserAgent).first()

    instance = AgentInstance(
        id=uuid4(),
        user_agent_id=user_agent.id,
        user_id=user.id,
        status=AgentStatus.ACTIVE,
        started_at=datetime.now(timezone.utc),
    )

    test_db.add(instance)
    test_db.commit()

    return instance


@pytest.fixture(autouse=True)
def reset_env():
    """Reset environment variables for each test."""
    original_env = os.environ.copy()
    yield
    os.environ.clear()
    os.environ.update(original_env)
