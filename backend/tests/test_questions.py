"""Tests for question endpoints."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import AgentQuestion, AgentInstance, User
from shared.database.enums import AgentStatus


class TestQuestionEndpoints:
    """Test question management endpoints."""

    def test_answer_question(
        self, authenticated_client, test_db, test_agent_instance, test_user
    ):
        """Test answering a pending question."""
        # Create a pending question
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Should I use async/await?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )
        test_db.add(question)
        test_db.commit()

        # Submit answer
        answer_text = "Yes, use async/await for better performance"
        response = authenticated_client.post(
            f"/api/v1/questions/{question.id}/answer", json={"answer": answer_text}
        )

        assert response.status_code == 200
        data = response.json()
        assert data["success"] is True
        assert data["message"] == "Answer submitted successfully"

        # Verify in database
        test_db.refresh(question)
        assert question.answer_text == answer_text
        assert question.answered_at is not None
        assert question.answered_by_user_id == test_user.id
        assert question.is_active is False

        # Verify agent instance status changed back to active
        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.ACTIVE

    def test_answer_question_not_found(self, authenticated_client):
        """Test answering a non-existent question."""
        fake_id = uuid4()
        response = authenticated_client.post(
            f"/api/v1/questions/{fake_id}/answer", json={"answer": "Some answer"}
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Question not found or already answered"

    def test_answer_already_answered_question(
        self, authenticated_client, test_db, test_agent_instance, test_user
    ):
        """Test answering an already answered question."""
        # Create an already answered question
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Already answered?",
            answer_text="Previous answer",
            asked_at=datetime.now(timezone.utc),
            answered_at=datetime.now(timezone.utc),
            answered_by_user_id=test_user.id,
            is_active=False,
        )
        test_db.add(question)
        test_db.commit()

        # Try to answer again
        response = authenticated_client.post(
            f"/api/v1/questions/{question.id}/answer", json={"answer": "New answer"}
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Question not found or already answered"

        # Verify answer didn't change
        test_db.refresh(question)
        assert question.answer_text == "Previous answer"

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

        # Create question for other user's agent
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=other_instance.id,
            question_text="Other user's question?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )
        test_db.add(question)
        test_db.commit()

        # Try to answer as current user
        response = authenticated_client.post(
            f"/api/v1/questions/{question.id}/answer",
            json={"answer": "Trying to answer"},
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Question not found or already answered"

        # Verify question remains unanswered
        test_db.refresh(question)
        assert question.answer_text is None
        assert question.is_active is True

    def test_answer_inactive_question(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test answering an inactive question."""
        # Create an inactive question (but not answered)
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Inactive question?",
            asked_at=datetime.now(timezone.utc),
            is_active=False,  # Inactive but not answered
        )
        test_db.add(question)
        test_db.commit()

        # Try to answer
        response = authenticated_client.post(
            f"/api/v1/questions/{question.id}/answer",
            json={"answer": "Trying to answer inactive"},
        )

        assert response.status_code == 404
        assert response.json()["detail"] == "Question not found or already answered"

    def test_answer_question_empty_answer(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test submitting an empty answer."""
        # Create a pending question
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Can I submit empty?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )
        test_db.add(question)
        test_db.commit()

        # Submit empty answer - should still work
        response = authenticated_client.post(
            f"/api/v1/questions/{question.id}/answer", json={"answer": ""}
        )

        assert response.status_code == 200

        # Verify empty answer was saved
        test_db.refresh(question)
        assert question.answer_text == ""
        assert question.is_active is False
