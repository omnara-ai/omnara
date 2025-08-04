"""Tests for shared core functionality."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import Message
from shared.database.enums import AgentStatus, SenderType


class TestDatabaseModels:
    """Test database model functionality."""

    def test_create_agent_instance(self, test_db, test_agent_instance):
        """Test creating an agent instance."""
        assert test_agent_instance.id is not None
        assert test_agent_instance.status == AgentStatus.ACTIVE
        assert test_agent_instance.started_at is not None
        assert test_agent_instance.ended_at is None

    def test_create_agent_messages(self, test_db, test_agent_instance):
        """Test creating agent messages."""
        message1 = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="First step",
            created_at=datetime.now(timezone.utc),
            requires_user_input=False,
        )

        message2 = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Second step",
            created_at=datetime.now(timezone.utc),
            requires_user_input=False,
        )

        test_db.add_all([message1, message2])
        test_db.commit()

        # Query messages
        messages = (
            test_db.query(Message)
            .filter_by(agent_instance_id=test_agent_instance.id)
            .order_by(Message.created_at)
            .all()
        )

        assert len(messages) == 2
        assert messages[0].content == "First step"
        assert messages[1].content == "Second step"
        assert all(msg.sender_type == SenderType.AGENT for msg in messages)

    def test_create_agent_question_message(self, test_db, test_agent_instance):
        """Test creating agent question as a message."""
        question = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Should I continue?",
            created_at=datetime.now(timezone.utc),
            requires_user_input=True,
        )

        test_db.add(question)
        test_db.commit()

        # Query question
        saved_question = test_db.query(Message).filter_by(id=question.id).first()

        assert saved_question is not None
        assert saved_question.content == "Should I continue?"
        assert saved_question.requires_user_input is True
        assert saved_question.sender_type == SenderType.AGENT

    def test_create_user_feedback_message(self, test_db, test_agent_instance):
        """Test creating user feedback as a message."""
        feedback = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.USER,
            content="Please use TypeScript",
            created_at=datetime.now(timezone.utc),
            requires_user_input=False,
            message_metadata={
                "source": "user_feedback",
                "created_by_user_id": str(test_agent_instance.user_id),
            },
        )

        test_db.add(feedback)
        test_db.commit()

        # Query feedback
        saved_feedback = test_db.query(Message).filter_by(id=feedback.id).first()

        assert saved_feedback is not None
        assert saved_feedback.content == "Please use TypeScript"
        assert saved_feedback.sender_type == SenderType.USER
        assert saved_feedback.message_metadata["created_by_user_id"] == str(
            test_agent_instance.user_id
        )

    def test_agent_instance_message_relationships(self, test_db, test_agent_instance):
        """Test agent instance message relationships."""
        # Add an agent message
        agent_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Test step",
            created_at=datetime.now(timezone.utc),
            requires_user_input=False,
        )

        # Add a question message
        question_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Test question?",
            created_at=datetime.now(timezone.utc),
            requires_user_input=True,
        )

        test_db.add_all([agent_msg, question_msg])
        test_db.commit()

        # Refresh instance to load relationships
        test_db.refresh(test_agent_instance)

        # Test relationships
        assert len(test_agent_instance.messages) == 2
        assert test_agent_instance.messages[0].content == "Test step"
        assert test_agent_instance.messages[1].content == "Test question?"
        assert test_agent_instance.messages[1].requires_user_input is True


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
