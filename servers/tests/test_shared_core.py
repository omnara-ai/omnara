"""Tests for shared core functionality."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import (
    AgentStep,
    AgentQuestion,
    AgentUserFeedback,
)
from shared.database.enums import AgentStatus


class TestDatabaseModels:
    """Test database model functionality."""

    def test_create_agent_instance(self, test_db, test_agent_instance):
        """Test creating an agent instance."""
        assert test_agent_instance.id is not None
        assert test_agent_instance.status == AgentStatus.ACTIVE
        assert test_agent_instance.started_at is not None
        assert test_agent_instance.ended_at is None

    def test_create_agent_step(self, test_db, test_agent_instance):
        """Test creating agent steps."""
        step1 = AgentStep(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            step_number=1,
            description="First step",
            created_at=datetime.now(timezone.utc),
        )

        step2 = AgentStep(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            step_number=2,
            description="Second step",
            created_at=datetime.now(timezone.utc),
        )

        test_db.add_all([step1, step2])
        test_db.commit()

        # Query steps
        steps = (
            test_db.query(AgentStep)
            .filter_by(agent_instance_id=test_agent_instance.id)
            .order_by(AgentStep.step_number)
            .all()
        )

        assert len(steps) == 2
        assert steps[0].step_number == 1
        assert steps[0].description == "First step"
        assert steps[1].step_number == 2
        assert steps[1].description == "Second step"

    def test_create_agent_question(self, test_db, test_agent_instance):
        """Test creating agent questions."""
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Should I continue?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )

        test_db.add(question)
        test_db.commit()

        # Query question
        saved_question = test_db.query(AgentQuestion).filter_by(id=question.id).first()

        assert saved_question is not None
        assert saved_question.question_text == "Should I continue?"
        assert saved_question.is_active is True
        assert saved_question.answer_text is None
        assert saved_question.answered_at is None

    def test_create_user_feedback(self, test_db, test_agent_instance):
        """Test creating user feedback."""
        feedback = AgentUserFeedback(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            created_by_user_id=test_agent_instance.user_id,
            feedback_text="Please use TypeScript",
            created_at=datetime.now(timezone.utc),
        )

        test_db.add(feedback)
        test_db.commit()

        # Query feedback
        saved_feedback = (
            test_db.query(AgentUserFeedback).filter_by(id=feedback.id).first()
        )

        assert saved_feedback is not None
        assert saved_feedback.feedback_text == "Please use TypeScript"
        assert saved_feedback.retrieved_at is None

    def test_agent_instance_relationships(self, test_db, test_agent_instance):
        """Test agent instance relationships."""
        # Add a step
        step = AgentStep(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            step_number=1,
            description="Test step",
            created_at=datetime.now(timezone.utc),
        )

        # Add a question
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Test question?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )

        test_db.add_all([step, question])
        test_db.commit()

        # Refresh instance to load relationships
        test_db.refresh(test_agent_instance)

        # Test relationships
        assert len(test_agent_instance.steps) == 1
        assert test_agent_instance.steps[0].description == "Test step"

        assert len(test_agent_instance.questions) == 1
        assert test_agent_instance.questions[0].question_text == "Test question?"


class TestAgentStatusTransitions:
    """Test agent status transitions."""

    def test_complete_agent_instance(self, test_db, test_agent_instance):
        """Test completing an agent instance."""
        # Complete the instance
        test_agent_instance.status = AgentStatus.COMPLETED
        test_agent_instance.ended_at = datetime.now(timezone.utc)
        test_db.commit()

        # Verify status change
        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.COMPLETED
        assert test_agent_instance.ended_at is not None

    def test_fail_agent_instance(self, test_db, test_agent_instance):
        """Test failing an agent instance."""
        # Fail the instance
        test_agent_instance.status = AgentStatus.FAILED
        test_agent_instance.ended_at = datetime.now(timezone.utc)
        test_db.commit()

        # Verify status change
        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.FAILED
        assert test_agent_instance.ended_at is not None
