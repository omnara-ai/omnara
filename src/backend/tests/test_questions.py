"""Tests for question endpoints."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import Message, AgentInstance, User
from shared.database.enums import AgentStatus, SenderType


class TestQuestionEndpoints:
    """Test question management endpoints."""

    def test_answer_question(
        self, authenticated_client, test_db, test_agent_instance, test_user
    ):
        """Test answering a pending question."""
        # Create a message with requires_user_input=True (question)
        from shared.database import Message, SenderType

        question_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Should I use async/await?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question_msg)

        # Set instance status to AWAITING_INPUT
        test_agent_instance.status = AgentStatus.AWAITING_INPUT
        test_db.commit()

        # Submit answer as a new message
        answer_text = "Yes, use async/await for better performance"
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": answer_text},
        )

        assert response.status_code == 200
        data = response.json()
        assert data["content"] == answer_text
        assert data["sender_type"] == "USER"
        assert data["requires_user_input"] is False

        # Verify agent instance status changed back to active
        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.ACTIVE

    def test_answer_question_not_found(self, authenticated_client):
        """Test sending message to non-existent agent instance."""
        fake_id = uuid4()
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{fake_id}/messages",
            json={"content": "Some answer"},
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Agent instance not found"

    def test_answer_already_answered_question(
        self, authenticated_client, test_db, test_agent_instance, test_user
    ):
        """Test that you can continue sending messages after answering a question."""
        # In the new system, you can always send more messages
        # This test verifies that behavior
        from shared.database import Message, SenderType

        # Create a question message
        question_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Already answered?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question_msg)

        # Create answer message
        answer_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.USER,
            content="Previous answer",
            requires_user_input=False,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(answer_msg)
        test_db.commit()

        # Send another message - should work fine
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "New message"},
        )

        assert response.status_code == 200
        assert response.json()["content"] == "New message"

    def test_answer_question_wrong_user(self, authenticated_client, test_db):
        """Test answering a question from another user's agent."""
        # Create another user and their agent instance
        other_user = User(
            id=uuid4(),
            email="other@example.com",
            display_name="Other User",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)

        # Create user agent for other user
        from shared.database.models import UserAgent

        other_user_agent = UserAgent(
            id=uuid4(),
            user_id=other_user.id,
            name="other agent",
            is_active=True,
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user_agent)

        other_instance = AgentInstance(
            id=uuid4(),
            user_agent_id=other_user_agent.id,
            user_id=other_user.id,
            status=AgentStatus.AWAITING_INPUT,
            started_at=datetime.now(timezone.utc),
        )
        test_db.add(other_instance)

        # Create question message for other user's agent
        question_msg = Message(
            id=uuid4(),
            agent_instance_id=other_instance.id,
            sender_type=SenderType.AGENT,
            content="Other user's question?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question_msg)
        test_db.commit()

        # Try to send message as current user to other user's instance
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{other_instance.id}/messages",
            json={"content": "Trying to answer"},
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Agent instance not found"

    def test_answer_inactive_question(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test sending message to completed agent instance."""
        # Set instance to COMPLETED status
        test_agent_instance.status = AgentStatus.COMPLETED
        test_agent_instance.ended_at = datetime.now(timezone.utc)
        test_db.commit()

        # Try to send message - should still work since status updates happen server-side
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "Message to completed instance"},
        )

        # Based on the backend code, messages can still be sent to completed instances
        assert response.status_code == 200

    def test_answer_question_empty_answer(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test submitting an empty message."""
        # Create a question message
        question_msg = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Can I submit empty?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question_msg)
        test_agent_instance.status = AgentStatus.AWAITING_INPUT
        test_db.commit()

        # Submit empty message - should still work
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": ""},
        )

        assert response.status_code == 200
        assert response.json()["content"] == ""
