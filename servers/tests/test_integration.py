"""Integration tests using PostgreSQL."""

import pytest
from datetime import datetime, timezone
from uuid import uuid4

# Database fixtures come from conftest.py

# Import the real models
from shared.database.models import (
    User,
    UserAgent,
    AgentInstance,
    AgentStep,
    AgentQuestion,
    AgentUserFeedback,
)
from shared.database.enums import AgentStatus

# Import the core functions we want to test
from servers.shared.core import (
    process_log_step,
    create_agent_question,
    process_end_session,
)


# Using test_db fixture from conftest.py which provides PostgreSQL via testcontainers


@pytest.fixture
def test_user(test_db):
    """Create a test user."""
    user = User(
        id=uuid4(),
        email="integration@test.com",
        display_name="Integration Test User",
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
        name="Claude Code Test",
        is_active=True,
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )
    test_db.add(user_agent)
    test_db.commit()
    return user_agent


class TestIntegrationFlow:
    """Test the complete integration flow with PostgreSQL."""

    @pytest.mark.integration
    @pytest.mark.asyncio
    async def test_complete_agent_session_flow(
        self, test_db, test_user, test_user_agent
    ):
        """Test a complete agent session from start to finish."""
        # Step 1: Create new agent instance
        instance_id, step_number, user_feedback = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=None,
            agent_type="Claude Code Test",
            step_description="Starting integration test task",
        )

        assert instance_id is not None
        assert step_number == 1
        assert user_feedback == []

        # Verify instance was created in database
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance is not None
        assert instance.status == AgentStatus.ACTIVE
        assert instance.user_id == test_user.id

        # Step 2: Log another step
        _, step_number2, _ = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=instance_id,
            agent_type="Claude Code Test",
            step_description="Processing files",
        )

        assert step_number2 == 2

        # Step 3: Create a question
        question = await create_agent_question(
            db=test_db,
            agent_instance_id=instance_id,
            question_text="Should I refactor this module?",
            user_id=str(test_user.id),
        )

        assert question is not None
        question_id = question.id

        # Verify question in database
        question = test_db.query(AgentQuestion).filter_by(id=question_id).first()
        assert question is not None
        assert question.question_text == "Should I refactor this module?"
        assert question.is_active is True

        # Step 4: Add user feedback
        feedback = AgentUserFeedback(
            id=uuid4(),
            agent_instance_id=instance_id,
            created_by_user_id=test_user.id,
            feedback_text="Please use async/await pattern",
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(feedback)
        test_db.commit()

        # Step 5: Next log_step should retrieve feedback
        _, step_number3, feedback_list = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=instance_id,
            agent_type="Claude Code Test",
            step_description="Implementing async pattern",
        )

        assert step_number3 == 3
        assert len(feedback_list) == 1
        assert feedback_list[0] == "Please use async/await pattern"

        # Verify feedback was marked as retrieved
        test_db.refresh(feedback)
        assert feedback.retrieved_at is not None

        # Step 6: End the session
        ended_instance_id, final_status = process_end_session(
            db=test_db, agent_instance_id=instance_id, user_id=str(test_user.id)
        )

        assert ended_instance_id == instance_id
        assert final_status == "completed"

        # Verify final state
        test_db.refresh(instance)
        assert instance.status == AgentStatus.COMPLETED
        assert instance.ended_at is not None

        # Verify questions were deactivated
        test_db.refresh(question)
        assert question.is_active is False

        # Verify all steps were logged
        steps = (
            test_db.query(AgentStep)
            .filter_by(agent_instance_id=instance_id)
            .order_by(AgentStep.step_number)
            .all()
        )

        assert len(steps) == 3
        assert steps[0].description == "Starting integration test task"
        assert steps[1].description == "Processing files"
        assert steps[2].description == "Implementing async pattern"

    @pytest.mark.integration
    def test_multiple_feedback_handling(self, test_db, test_user, test_user_agent):
        """Test handling multiple feedback items."""
        # Create instance
        instance_id, _, _ = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=None,
            agent_type="Claude Code Test",
            step_description="Starting task",
        )

        # Add multiple feedback items
        feedback_items = []
        for i in range(3):
            feedback = AgentUserFeedback(
                id=uuid4(),
                agent_instance_id=instance_id,
                created_by_user_id=test_user.id,
                feedback_text=f"Feedback {i + 1}",
                created_at=datetime.now(timezone.utc),
            )
            feedback_items.append(feedback)
            test_db.add(feedback)

        test_db.commit()

        # Next log_step should retrieve all feedback
        _, _, feedback_list = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=instance_id,
            agent_type="Claude Code Test",
            step_description="Processing feedback",
        )

        assert len(feedback_list) == 3
        assert set(feedback_list) == {"Feedback 1", "Feedback 2", "Feedback 3"}

        # All feedback should be marked as retrieved
        for feedback in feedback_items:
            test_db.refresh(feedback)
            assert feedback.retrieved_at is not None

        # Subsequent log_step should not retrieve same feedback
        _, _, feedback_list2 = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=instance_id,
            agent_type="Claude Code Test",
            step_description="Continuing work",
        )

        assert len(feedback_list2) == 0

    @pytest.mark.integration
    def test_user_agent_creation_and_reuse(self, test_db, test_user):
        """Test that user agents are created and reused correctly."""
        # First call should create a new user agent
        instance1_id, _, _ = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=None,
            agent_type="New Agent Type",
            step_description="First task",
        )

        # Check user agent was created (name is stored in lowercase)
        user_agents = (
            test_db.query(UserAgent)
            .filter_by(user_id=test_user.id, name="new agent type")
            .all()
        )
        assert len(user_agents) == 1

        # Second call with same agent type should reuse the user agent
        instance2_id, _, _ = process_log_step(
            db=test_db,
            user_id=str(test_user.id),
            agent_instance_id=None,
            agent_type="New Agent Type",
            step_description="Second task",
        )

        # Should still only have one user agent (name is stored in lowercase)
        user_agents = (
            test_db.query(UserAgent)
            .filter_by(user_id=test_user.id, name="new agent type")
            .all()
        )
        assert len(user_agents) == 1

        # But two different instances
        assert instance1_id != instance2_id

        # Both instances should reference the same user agent
        instance1 = test_db.query(AgentInstance).filter_by(id=instance1_id).first()
        instance2 = test_db.query(AgentInstance).filter_by(id=instance2_id).first()
        assert instance1.user_agent_id == instance2.user_agent_id
