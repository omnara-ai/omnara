"""Tests for agent endpoints."""

from datetime import datetime, timezone
from uuid import uuid4

from shared.database.models import (
    User,
    UserAgent,
    AgentInstance,
    AgentStep,
    AgentQuestion,
    AgentUserFeedback,
)
from shared.database.enums import AgentStatus


class TestAgentEndpoints:
    """Test agent management endpoints."""

    def test_list_agent_types(
        self, authenticated_client, test_user_agent, test_agent_instance
    ):
        """Test listing agent types with instances."""
        response = authenticated_client.get("/api/v1/agent-types")
        assert response.status_code == 200
        data = response.json()

        assert len(data) == 1
        agent_type = data[0]
        assert agent_type["id"] == str(test_user_agent.id)
        assert agent_type["name"] == "claude code"
        assert len(agent_type["recent_instances"]) == 1
        assert agent_type["recent_instances"][0]["id"] == str(test_agent_instance.id)

    def test_list_agent_types_multiple_users(
        self, authenticated_client, test_db, test_user_agent
    ):
        """Test that users only see their own agent types."""
        # Create another user with agent
        other_user = User(
            id=uuid4(),
            email="other@example.com",
            display_name="Other User",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)

        other_user_agent = UserAgent(
            id=uuid4(),
            user_id=other_user.id,
            name="cursor",
            is_active=True,
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user_agent)
        test_db.commit()

        response = authenticated_client.get("/api/v1/agent-types")
        assert response.status_code == 200
        data = response.json()

        # Should only see own agent type
        assert len(data) == 1
        assert data[0]["name"] == "claude code"

    def test_list_agent_types_with_pending_questions(
        self, authenticated_client, test_db, test_user, test_user_agent
    ):
        """Test agent types listing with pending questions (catches timezone issues)."""
        # Create an agent instance
        instance = AgentInstance(
            id=uuid4(),
            user_agent_id=test_user_agent.id,
            user_id=test_user.id,
            status=AgentStatus.ACTIVE,
            started_at=datetime.now(timezone.utc),
        )
        test_db.add(instance)

        # Create a pending question with timezone-aware datetime
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=instance.id,
            question_text="Test question?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )
        test_db.add(question)
        test_db.commit()

        # This should not raise a timezone error
        response = authenticated_client.get("/api/v1/agent-types")
        assert response.status_code == 200
        data = response.json()

        assert len(data) == 1
        agent_type = data[0]
        assert len(agent_type["recent_instances"]) == 1

        # Check that pending question info is populated
        instance_data = agent_type["recent_instances"][0]
        assert instance_data["has_pending_question"] is True
        assert instance_data["pending_questions_count"] == 1
        assert instance_data["pending_question_age"] is not None
        assert instance_data["pending_question_age"] >= 0

    def test_list_all_agent_instances(self, authenticated_client, test_agent_instance):
        """Test listing all agent instances."""
        response = authenticated_client.get("/api/v1/agent-instances")
        assert response.status_code == 200
        data = response.json()

        assert len(data) == 1
        instance = data[0]
        assert instance["id"] == str(test_agent_instance.id)
        assert instance["status"] == "active"

    def test_list_agent_instances_with_limit(
        self, authenticated_client, test_db, test_user, test_user_agent
    ):
        """Test listing agent instances with limit."""
        # Create multiple instances
        for i in range(5):
            instance = AgentInstance(
                id=uuid4(),
                user_agent_id=test_user_agent.id,
                user_id=test_user.id,
                status=AgentStatus.COMPLETED,
                started_at=datetime.now(timezone.utc),
            )
            test_db.add(instance)
        test_db.commit()

        response = authenticated_client.get("/api/v1/agent-instances?limit=3")
        assert response.status_code == 200
        data = response.json()
        assert len(data) == 3

    def test_get_agent_summary(
        self,
        authenticated_client,
        test_db,
        test_user,
        test_user_agent,
        test_agent_instance,
    ):
        """Test getting agent summary."""
        # Add more instances with different statuses
        completed_instance = AgentInstance(
            id=uuid4(),
            user_agent_id=test_user_agent.id,
            user_id=test_user.id,
            status=AgentStatus.COMPLETED,
            started_at=datetime.now(timezone.utc),
            ended_at=datetime.now(timezone.utc),
        )
        test_db.add(completed_instance)

        # Add a question to the active instance
        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Test question?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )
        test_db.add(question)
        test_db.commit()

        response = authenticated_client.get("/api/v1/agent-summary")
        assert response.status_code == 200
        data = response.json()

        assert data["total_instances"] == 2
        assert data["active_instances"] == 1
        assert data["completed_instances"] == 1
        assert "agent_types" in data
        assert len(data["agent_types"]) == 1

    def test_get_type_instances(
        self, authenticated_client, test_user_agent, test_agent_instance
    ):
        """Test getting instances for a specific agent type."""
        response = authenticated_client.get(
            f"/api/v1/agent-types/{test_user_agent.id}/instances"
        )
        assert response.status_code == 200
        data = response.json()

        assert len(data) == 1
        assert data[0]["id"] == str(test_agent_instance.id)

    def test_get_type_instances_not_found(self, authenticated_client):
        """Test getting instances for non-existent agent type."""
        fake_id = uuid4()
        response = authenticated_client.get(f"/api/v1/agent-types/{fake_id}/instances")
        assert response.status_code == 404
        assert response.json()["detail"] == "Agent type not found"

    def test_get_instance_detail(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test getting detailed agent instance information."""
        # Add steps and questions
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

        question = AgentQuestion(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            question_text="Need input?",
            asked_at=datetime.now(timezone.utc),
            is_active=True,
        )

        feedback = AgentUserFeedback(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            created_by_user_id=test_agent_instance.user_id,
            feedback_text="Great work!",
            created_at=datetime.now(timezone.utc),
        )

        test_db.add_all([step1, step2, question, feedback])
        test_db.commit()

        response = authenticated_client.get(
            f"/api/v1/agent-instances/{test_agent_instance.id}"
        )
        assert response.status_code == 200
        data = response.json()

        assert data["id"] == str(test_agent_instance.id)
        assert len(data["steps"]) == 2
        assert data["steps"][0]["description"] == "First step"
        assert len(data["questions"]) == 1
        assert data["questions"][0]["question_text"] == "Need input?"
        assert len(data["user_feedback"]) == 1
        assert data["user_feedback"][0]["feedback_text"] == "Great work!"

    def test_get_instance_detail_not_found(self, authenticated_client):
        """Test getting non-existent instance detail."""
        fake_id = uuid4()
        response = authenticated_client.get(f"/api/v1/agent-instances/{fake_id}")
        assert response.status_code == 404
        assert response.json()["detail"] == "Agent instance not found"

    def test_add_user_feedback(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test adding user feedback to an agent instance."""
        feedback_text = "Please use TypeScript for this component"
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/feedback",
            json={"feedback": feedback_text},
        )

        assert response.status_code == 200
        data = response.json()
        assert data["feedback_text"] == feedback_text
        assert "id" in data
        assert "created_at" in data

        # Verify in database
        feedback = (
            test_db.query(AgentUserFeedback)
            .filter_by(agent_instance_id=test_agent_instance.id)
            .first()
        )
        assert feedback is not None
        assert feedback.feedback_text == feedback_text
        assert feedback.retrieved_at is None

    def test_add_feedback_to_nonexistent_instance(self, authenticated_client):
        """Test adding feedback to non-existent instance."""
        fake_id = uuid4()
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{fake_id}/feedback",
            json={"feedback": "Test feedback"},
        )
        assert response.status_code == 404
        assert response.json()["detail"] == "Agent instance not found"

    def test_update_agent_status_completed(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test marking an agent instance as completed."""
        response = authenticated_client.put(
            f"/api/v1/agent-instances/{test_agent_instance.id}/status",
            json={"status": "completed"},
        )

        assert response.status_code == 200
        data = response.json()
        assert data["status"] == "completed"
        assert data["ended_at"] is not None

        # Verify in database
        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.COMPLETED
        assert test_agent_instance.ended_at is not None

    def test_update_agent_status_unsupported(
        self, authenticated_client, test_agent_instance
    ):
        """Test unsupported status update."""
        response = authenticated_client.put(
            f"/api/v1/agent-instances/{test_agent_instance.id}/status",
            json={"status": "paused"},
        )
        assert response.status_code == 400
        assert response.json()["detail"] == "Status update not supported"

    def test_update_status_nonexistent_instance(self, authenticated_client):
        """Test updating status of non-existent instance."""
        fake_id = uuid4()
        response = authenticated_client.put(
            f"/api/v1/agent-instances/{fake_id}/status", json={"status": "completed"}
        )
        assert response.status_code == 404
        assert response.json()["detail"] == "Agent instance not found"
