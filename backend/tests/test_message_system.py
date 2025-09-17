"""Comprehensive tests for the unified message system."""

from datetime import datetime, timezone
from uuid import uuid4
import time

from shared.database.models import Message, AgentInstance, User
from shared.database.enums import AgentStatus, SenderType
from backend.main import app
from backend.auth.dependencies import get_current_user, get_optional_current_user


class TestMessageSystem:
    """Test the core message system functionality."""

    def test_message_flow_creates_conversation(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test that messages create a proper conversation flow."""
        # Agent sends initial message
        agent_msg1 = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="I'm starting to work on your request",
            requires_user_input=False,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(agent_msg1)
        test_db.commit()

        # Agent asks a question
        agent_question = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Which framework would you prefer: React or Vue?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(agent_question)
        test_agent_instance.status = AgentStatus.AWAITING_INPUT
        test_db.commit()

        # User responds
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "Let's use React with TypeScript"},
        )
        assert response.status_code == 200
        user_response = response.json()
        assert user_response["sender_type"] == "USER"
        assert user_response["requires_user_input"] is False

        # Verify conversation in detail endpoint
        detail_response = authenticated_client.get(
            f"/api/v1/agent-instances/{test_agent_instance.id}"
        )
        assert detail_response.status_code == 200
        detail = detail_response.json()

        assert len(detail["messages"]) == 3
        assert (
            detail["messages"][0]["content"] == "I'm starting to work on your request"
        )
        assert (
            detail["messages"][1]["content"]
            == "Which framework would you prefer: React or Vue?"
        )
        assert detail["messages"][1]["requires_user_input"] is True
        assert detail["messages"][2]["content"] == "Let's use React with TypeScript"
        assert detail["messages"][2]["sender_type"] == "USER"

    def test_user_message_response_includes_sender_metadata(
        self, authenticated_client, test_user, test_agent_instance
    ):
        """Sending a message should surface sender metadata in responses."""
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "Checking sender metadata"},
        )
        assert response.status_code == 200
        payload = response.json()
        assert payload["sender_user_id"] == str(test_user.id)
        assert payload["sender_user_email"] == test_user.email
        assert payload["sender_user_display_name"] == test_user.display_name

        detail_response = authenticated_client.get(
            f"/api/v1/agent-instances/{test_agent_instance.id}"
        )
        assert detail_response.status_code == 200
        messages = detail_response.json()["messages"]
        last_message = messages[-1]
        assert last_message["sender_user_id"] == str(test_user.id)
        assert last_message["sender_user_email"] == test_user.email

    def test_multiple_user_messages_allowed(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test that users can send multiple messages in a row."""
        # Send first user message
        response1 = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "First instruction"},
        )
        assert response1.status_code == 200

        # Send second user message immediately
        response2 = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "Additional instruction"},
        )
        assert response2.status_code == 200

        # Send third user message
        response3 = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "One more thing..."},
        )
        assert response3.status_code == 200

        # Verify all messages were created
        messages = (
            test_db.query(Message)
            .filter_by(
                agent_instance_id=test_agent_instance.id, sender_type=SenderType.USER
            )
            .all()
        )
        assert len(messages) == 3
        assert [msg.content for msg in messages] == [
            "First instruction",
            "Additional instruction",
            "One more thing...",
        ]

    def test_message_ordering_preserved(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test that message ordering is preserved correctly."""
        # Create messages with specific order
        messages_data = [
            (SenderType.AGENT, "Starting task", False),
            (SenderType.AGENT, "Found an issue", False),
            (SenderType.AGENT, "How should I proceed?", True),
            (SenderType.USER, "Fix it with option A", False),
            (SenderType.AGENT, "Implementing option A", False),
        ]

        created_messages = []
        for i, (sender_type, content, requires_input) in enumerate(messages_data):
            time.sleep(0.01)  # Ensure different timestamps

            if sender_type == SenderType.AGENT:
                msg = Message(
                    id=uuid4(),
                    agent_instance_id=test_agent_instance.id,
                    sender_type=sender_type,
                    content=content,
                    requires_user_input=requires_input,
                    created_at=datetime.now(timezone.utc),
                )
                test_db.add(msg)
                test_db.commit()
                created_messages.append(msg)
            else:
                # User message via API
                response = authenticated_client.post(
                    f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
                    json={"content": content},
                )
                assert response.status_code == 200

        # Get instance detail
        response = authenticated_client.get(
            f"/api/v1/agent-instances/{test_agent_instance.id}"
        )
        assert response.status_code == 200
        data = response.json()

        # Verify order is preserved
        assert len(data["messages"]) == 5
        for i, (sender_type, content, requires_input) in enumerate(messages_data):
            msg = data["messages"][i]
            assert msg["content"] == content
            assert msg["sender_type"] == sender_type.value
            if sender_type == SenderType.AGENT:
                assert msg["requires_user_input"] == requires_input

    def test_agent_instance_summary_includes_message_stats(
        self, authenticated_client, test_db, test_user, test_user_agent
    ):
        """Test that agent instance summaries include message statistics."""
        # Create multiple instances with different message counts
        instances = []
        for i in range(3):
            instance = AgentInstance(
                id=uuid4(),
                user_agent_id=test_user_agent.id,
                user_id=test_user.id,
                status=AgentStatus.ACTIVE,
                started_at=datetime.now(timezone.utc),
            )
            test_db.add(instance)
            instances.append(instance)

            # Add different number of messages to each instance
            for j in range(i + 1):
                msg = Message(
                    id=uuid4(),
                    agent_instance_id=instance.id,
                    sender_type=SenderType.AGENT,
                    content=f"Message {j} for instance {i}",
                    requires_user_input=False,
                    created_at=datetime.now(timezone.utc),
                )
                test_db.add(msg)

        test_db.commit()

        # Get agent types overview
        response = authenticated_client.get("/api/v1/agent-types")
        assert response.status_code == 200
        data = response.json()

        assert len(data) == 1
        agent_type = data[0]
        assert len(agent_type["recent_instances"]) >= 3

        # Verify chat_length is included
        for instance in agent_type["recent_instances"]:
            assert "chat_length" in instance
            assert instance["chat_length"] >= 0

    def test_message_with_empty_content_allowed(
        self, authenticated_client, test_agent_instance
    ):
        """Test that empty messages are allowed."""
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": ""},
        )
        assert response.status_code == 200
        data = response.json()
        assert data["content"] == ""
        assert data["sender_type"] == "USER"

    def test_message_status_transitions(
        self, authenticated_client, test_db, test_agent_instance
    ):
        """Test that message flow properly triggers status transitions."""
        # Start with ACTIVE
        assert test_agent_instance.status == AgentStatus.ACTIVE

        # Agent asks question -> AWAITING_INPUT
        question = Message(
            id=uuid4(),
            agent_instance_id=test_agent_instance.id,
            sender_type=SenderType.AGENT,
            content="Should I continue?",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question)
        test_agent_instance.status = AgentStatus.AWAITING_INPUT
        test_db.commit()

        # User responds -> back to ACTIVE
        response = authenticated_client.post(
            f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
            json={"content": "Yes, continue"},
        )
        assert response.status_code == 200

        test_db.refresh(test_agent_instance)
        assert test_agent_instance.status == AgentStatus.ACTIVE

    def test_awaiting_input_instances_prioritized(
        self, authenticated_client, test_db, test_user, test_user_agent
    ):
        """Test that instances awaiting input are prioritized in listings."""
        # Create active instance
        active_instance = AgentInstance(
            id=uuid4(),
            user_agent_id=test_user_agent.id,
            user_id=test_user.id,
            status=AgentStatus.ACTIVE,
            started_at=datetime.now(timezone.utc),
        )
        test_db.add(active_instance)

        # Create awaiting input instance (older)
        awaiting_instance = AgentInstance(
            id=uuid4(),
            user_agent_id=test_user_agent.id,
            user_id=test_user.id,
            status=AgentStatus.AWAITING_INPUT,
            started_at=datetime.now(timezone.utc),
        )
        test_db.add(awaiting_instance)

        # Add question message to awaiting instance
        question = Message(
            id=uuid4(),
            agent_instance_id=awaiting_instance.id,
            sender_type=SenderType.AGENT,
            content="Need your input",
            requires_user_input=True,
            created_at=datetime.now(timezone.utc),
        )
        test_db.add(question)
        test_db.commit()

        # Get agent types
        response = authenticated_client.get("/api/v1/agent-types")
        assert response.status_code == 200
        data = response.json()

        # Awaiting input instance should be first
        instances = data[0]["recent_instances"]
        assert len(instances) >= 2
        assert instances[0]["status"] == "AWAITING_INPUT"
        assert instances[0]["latest_message"] == "Need your input"

    def test_user_message_requires_write_access(
        self, client, test_db, test_agent_instance
    ):
        """Users without write access should not be able to send messages."""

        other_user = User(
            id=uuid4(),
            email="unauthorized@example.com",
            display_name="Unauthorized",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)
        test_db.commit()

        def override_current_user():
            return other_user

        app.dependency_overrides[get_current_user] = override_current_user
        app.dependency_overrides[get_optional_current_user] = override_current_user

        try:
            response = client.post(
                f"/api/v1/agent-instances/{test_agent_instance.id}/messages",
                json={"content": "Attempting to write"},
            )
            assert response.status_code == 404
        finally:
            if get_current_user in app.dependency_overrides:
                del app.dependency_overrides[get_current_user]
            if get_optional_current_user in app.dependency_overrides:
                del app.dependency_overrides[get_optional_current_user]
