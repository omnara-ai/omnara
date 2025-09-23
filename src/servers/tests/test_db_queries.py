"""Tests for database query functions using PostgreSQL."""

import pytest
import asyncio
from datetime import datetime, timezone
from uuid import uuid4, UUID

# Database fixtures come from conftest.py

# Import the real models
from shared.database.models import (
    User,
    UserAgent,
    AgentInstance,
    Message,
)
from shared.database.enums import AgentStatus, SenderType

# Import the functions we want to test
from servers.shared.db import (
    send_agent_message,
    end_session,
    create_agent_message,
    create_user_message,
    wait_for_answer,
    get_queued_user_messages,
    get_or_create_agent_instance,
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
        name="claude code test",  # lowercase as per normalization
        is_active=True,
        created_at=datetime.now(timezone.utc),
        updated_at=datetime.now(timezone.utc),
    )
    test_db.add(user_agent)
    test_db.commit()
    return user_agent


class TestMessageIntegration:
    """Test the unified message system with PostgreSQL."""

    @pytest.mark.integration
    @pytest.mark.asyncio
    async def test_complete_agent_session_with_messages(self, test_db, test_user):
        """Test a complete agent session using the unified message system."""
        # Step 1: Send first agent message (creates new instance)
        instance_id, message_id, queued_messages = await send_agent_message(
            db=test_db,
            agent_instance_id=str(uuid4()),  # Client-generated UUID
            content="Starting integration test task",
            user_id=str(test_user.id),
            agent_type="Claude Code Test",
            requires_user_input=False,
        )
        test_db.commit()

        assert instance_id is not None
        assert message_id is not None
        assert queued_messages == []

        # Verify instance was created
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance is not None
        assert instance.status == AgentStatus.ACTIVE
        assert instance.user_id == test_user.id

        # Verify message was created
        message = test_db.query(Message).filter_by(id=message_id).first()
        assert message is not None
        assert message.content == "Starting integration test task"
        assert message.sender_type == SenderType.AGENT
        assert not message.requires_user_input

        # Step 2: Send a question (requires user input)
        _, question_id, _ = await send_agent_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Should I refactor this module?",
            user_id=str(test_user.id),
            requires_user_input=True,
        )
        test_db.commit()

        # Verify instance status changed
        test_db.refresh(instance)
        assert instance.status == AgentStatus.AWAITING_INPUT

        # Step 3: Simulate user response
        user_message = Message(
            agent_instance_id=UUID(instance_id),
            sender_type=SenderType.USER,
            content="Yes, please use async/await pattern",
            requires_user_input=False,
        )
        test_db.add(user_message)
        test_db.commit()

        # Step 4: Agent polls and gets the user message
        _, next_message_id, queued_user_msgs = await send_agent_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Implementing async pattern as requested",
            user_id=str(test_user.id),
            requires_user_input=False,
        )
        test_db.commit()

        assert len(queued_user_msgs) == 1
        assert queued_user_msgs[0].content == "Yes, please use async/await pattern"

        # Step 5: End the session
        ended_instance_id, final_status = end_session(
            db=test_db,
            agent_instance_id=instance_id,
            user_id=str(test_user.id),
        )
        test_db.commit()

        assert ended_instance_id == instance_id
        assert final_status == "COMPLETED"

        # Verify final state
        test_db.refresh(instance)
        assert instance.status == AgentStatus.COMPLETED
        assert instance.ended_at is not None

        # Verify all messages are in the database
        messages = (
            test_db.query(Message)
            .filter_by(agent_instance_id=instance_id)
            .order_by(Message.created_at)
            .all()
        )

        assert len(messages) == 4  # 2 agent messages, 1 question, 1 user response
        assert messages[0].content == "Starting integration test task"
        assert messages[1].content == "Should I refactor this module?"
        assert messages[2].content == "Yes, please use async/await pattern"
        assert messages[3].content == "Implementing async pattern as requested"

    @pytest.mark.integration
    @pytest.mark.asyncio
    async def test_wait_for_answer_functionality(self, test_db, test_user):
        """Test the wait_for_answer polling mechanism."""
        # Create an agent instance using get_or_create_agent_instance
        instance_id = str(uuid4())
        instance = get_or_create_agent_instance(
            test_db, instance_id, str(test_user.id), agent_type="Test Agent"
        )
        test_db.commit()

        # Create a question message
        question = create_agent_message(
            db=test_db,
            instance_id=instance.id,
            content="What should I do next?",
            requires_user_input=True,
        )
        test_db.commit()

        # Start waiting for answer in background
        async def wait_task():
            return await wait_for_answer(
                db=test_db,
                question_id=question.id,
                timeout_seconds=5,
            )

        wait_future = asyncio.create_task(wait_task())

        # Wait a bit then add user response
        await asyncio.sleep(1)

        user_response = Message(
            agent_instance_id=instance.id,
            sender_type=SenderType.USER,
            content="Please add error handling",
            requires_user_input=False,
        )
        test_db.add(user_response)
        test_db.commit()

        # Get the answer
        answer = await wait_future
        assert answer == "Please add error handling"

        # Verify last_read_message_id is still the question (wait_for_answer doesn't update it)
        test_db.refresh(instance)
        assert instance.last_read_message_id == question.id

    def test_create_agent_message_invalid_metadata(self, test_db, test_user):
        """Ensure invalid structured metadata is rejected."""

        instance_id = str(uuid4())
        instance = get_or_create_agent_instance(
            test_db, instance_id, str(test_user.id), agent_type="Test Agent"
        )
        test_db.commit()

        with pytest.raises(ValueError):
            create_agent_message(
                db=test_db,
                instance_id=instance.id,
                content="Attempt with bad metadata",
                message_metadata={
                    "message_type": "CODE_EDIT",
                    "payload": {"not_edits": []},
                },
            )

    @pytest.mark.integration
    def test_multiple_user_messages_queuing(self, test_db, test_user):
        """Test handling multiple queued user messages."""
        # Create instance with initial message
        instance_id, first_msg_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Initial message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Add multiple user messages while agent is "working"
        user_messages = []
        for i in range(3):
            msg = Message(
                agent_instance_id=UUID(instance_id),
                sender_type=SenderType.USER,
                content=f"User feedback {i + 1}",
                requires_user_input=False,
            )
            user_messages.append(msg)
            test_db.add(msg)
        test_db.commit()

        # Agent sends next message and should get all queued messages
        _, _, queued_messages = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Processing feedback",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()

        assert len(queued_messages) == 3
        assert set(msg.content for msg in queued_messages) == {
            "User feedback 1",
            "User feedback 2",
            "User feedback 3",
        }

        # Next agent message should get no queued messages
        _, _, queued_messages2 = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Continuing work",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()

        assert len(queued_messages2) == 0

    @pytest.mark.integration
    def test_concurrent_message_reading(self, test_db, test_user):
        """Test handling of concurrent message reads (stale detection)."""
        # Create instance and first message
        instance_id, first_msg_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Initial message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Add a user message
        user_msg = Message(
            agent_instance_id=UUID(instance_id),
            sender_type=SenderType.USER,
            content="User response",
            requires_user_input=False,
        )
        test_db.add(user_msg)
        test_db.commit()

        # Simulate polling endpoint: get pending messages with specific last_read_message_id
        # This simulates the endpoint checking for new messages with the last known ID
        response = test_db.query(AgentInstance).filter_by(id=instance_id).first()

        # Get messages - pass the current last_read_message_id
        messages1 = get_queued_user_messages(
            db=test_db,
            instance_id=UUID(instance_id),
            last_read_message_id=response.last_read_message_id,
        )

        # Should get the user message
        assert messages1 is not None
        assert len(messages1) == 1
        assert messages1[0].content == "User response"

        # Commit to save the updated last_read_message_id
        test_db.commit()

        # Try again with the OLD last_read_message_id (simulating stale client)
        messages2 = get_queued_user_messages(
            db=test_db,
            instance_id=UUID(instance_id),
            last_read_message_id=UUID(first_msg_id),  # Using the original message ID
        )

        # Should return None indicating stale read
        assert messages2 is None

    @pytest.mark.integration
    def test_git_diff_handling(self, test_db, test_user):
        """Test git diff sanitization and storage."""
        git_diff = """diff --git a/test.py b/test.py
index 1234567..abcdefg 100644
--- a/test.py
+++ b/test.py
@@ -1,3 +1,3 @@
-def hello():
-    print("Hello")
+def hello():
+    print("Hello, World!")
"""

        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Updated greeting function",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
                git_diff=git_diff,
            )
        )
        test_db.commit()

        # Verify git diff was stored (stripped of trailing whitespace)
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.git_diff == git_diff.strip()

        # Test clearing git diff
        asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Cleared changes",
                user_id=str(test_user.id),
                requires_user_input=False,
                git_diff="",  # Empty string clears the diff
            )
        )
        test_db.commit()

        test_db.refresh(instance)
        assert instance.git_diff == ""

    @pytest.mark.integration
    def test_agent_type_normalization(self, test_db, test_user):
        """Test that agent types are normalized to lowercase."""
        # Create instances with different case variations
        instance1_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Test 1",
                user_id=str(test_user.id),
                agent_type="Claude Code",
                requires_user_input=False,
            )
        )
        test_db.commit()

        instance2_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Test 2",
                user_id=str(test_user.id),
                agent_type="CLAUDE CODE",
                requires_user_input=False,
            )
        )
        test_db.commit()

        instance3_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Test 3",
                user_id=str(test_user.id),
                agent_type="claude code",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Commit to ensure all data is persisted
        test_db.commit()

        # All should use the same user agent (normalized to lowercase)
        user_agents = (
            test_db.query(UserAgent)
            .filter_by(user_id=test_user.id, name="claude code")
            .all()
        )

        assert len(user_agents) == 1

        # Verify all instances use the same user agent
        instance1 = test_db.query(AgentInstance).filter_by(id=instance1_id).first()
        instance2 = test_db.query(AgentInstance).filter_by(id=instance2_id).first()
        instance3 = test_db.query(AgentInstance).filter_by(id=instance3_id).first()

        assert (
            instance1.user_agent_id
            == instance2.user_agent_id
            == instance3.user_agent_id
        )

    @pytest.mark.integration
    def test_access_control(self, test_db, test_user):
        """Test that users can only access their own agent instances."""
        # Create another user
        other_user = User(
            id=uuid4(),
            email="other@test.com",
            display_name="Other User",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)
        test_db.commit()

        # Create instance for first user
        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="User 1 message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Try to access with other user - should fail
        with pytest.raises(ValueError, match="Access denied"):
            asyncio.run(
                send_agent_message(
                    db=test_db,
                    agent_instance_id=instance_id,
                    content="Unauthorized access attempt",
                    user_id=str(other_user.id),
                    requires_user_input=False,
                )
            )

    @pytest.mark.integration
    @pytest.mark.asyncio
    async def test_message_ordering(self, test_db, test_user):
        """Test that messages maintain proper ordering."""
        instance_id = str(uuid4())

        # Send multiple messages with small delays
        message_contents = []
        for i in range(5):
            content = f"Message {i + 1}"
            message_contents.append(content)
            await send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content=content,
                user_id=str(test_user.id),
                agent_type="Test Agent" if i == 0 else None,
                requires_user_input=False,
            )
            test_db.commit()
            # Small delay to ensure different timestamps
            await asyncio.sleep(0.01)

        # Retrieve all messages
        messages = (
            test_db.query(Message)
            .filter_by(agent_instance_id=instance_id)
            .order_by(Message.created_at)
            .all()
        )

        assert len(messages) == 5
        for i, msg in enumerate(messages):
            assert msg.content == message_contents[i]

    @pytest.mark.integration
    def test_status_transitions(self, test_db, test_user):
        """Test agent instance status transitions."""
        instance_id = str(uuid4())

        # Initial message - should be ACTIVE
        asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Starting work",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.status == AgentStatus.ACTIVE

        # Question - should change to AWAITING_INPUT
        asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Need user input",
                user_id=str(test_user.id),
                requires_user_input=True,
            )
        )
        test_db.commit()

        test_db.refresh(instance)
        assert instance.status == AgentStatus.AWAITING_INPUT

        # Regular message - should go back to ACTIVE
        asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Got input, continuing",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()

        test_db.refresh(instance)
        assert instance.status == AgentStatus.ACTIVE

        # End session - should be COMPLETED
        end_session(
            db=test_db,
            agent_instance_id=instance_id,
            user_id=str(test_user.id),
        )
        test_db.commit()

        test_db.refresh(instance)
        assert instance.status == AgentStatus.COMPLETED

        # Messages after completion should not change status
        asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Message after completion",
                user_id=str(test_user.id),
                requires_user_input=True,
            )
        )
        test_db.commit()

        test_db.refresh(instance)
        assert instance.status == AgentStatus.COMPLETED  # Should remain completed

    @pytest.mark.integration
    def test_create_user_message_with_mark_as_read(self, test_db, test_user):
        """Test creating a user message with mark_as_read=True."""
        # Create an agent instance first
        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Initial agent message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Get the instance to check initial last_read_message_id
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        initial_last_read_id = instance.last_read_message_id

        # Create a user message with mark_as_read=True (default)
        result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User response message",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        test_db.commit()

        # Extract values from result dictionary
        message_id = result["id"]
        marked_as_read = result["marked_as_read"]

        # Verify the message was created
        message = test_db.query(Message).filter_by(id=message_id).first()
        assert message is not None
        assert message.content == "User response message"
        assert message.sender_type == SenderType.USER
        assert not message.requires_user_input

        # Verify marked_as_read is True
        assert marked_as_read is True

        # Verify last_read_message_id was updated
        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(message_id)
        assert instance.last_read_message_id != initial_last_read_id

    @pytest.mark.integration
    def test_create_user_message_without_mark_as_read(self, test_db, test_user):
        """Test creating a user message with mark_as_read=False."""
        # Create an agent instance first
        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Initial agent message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Get the instance to check initial last_read_message_id
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        initial_last_read_id = instance.last_read_message_id

        # Create a user message with mark_as_read=False
        result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Unread user message",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        test_db.commit()

        # Extract values from result dictionary
        message_id = result["id"]
        marked_as_read = result["marked_as_read"]

        # Verify the message was created
        message = test_db.query(Message).filter_by(id=message_id).first()
        assert message is not None
        assert message.content == "Unread user message"
        assert message.sender_type == SenderType.USER

        # Verify marked_as_read is False
        assert marked_as_read is False

        # Verify last_read_message_id was NOT updated
        test_db.refresh(instance)
        assert instance.last_read_message_id == initial_last_read_id
        assert instance.last_read_message_id != UUID(message_id)

    @pytest.mark.integration
    def test_create_user_message_access_control(self, test_db, test_user):
        """Test that users can only create messages for their own instances."""
        # Create another user
        other_user = User(
            id=uuid4(),
            email="other2@test.com",
            display_name="Other User 2",
            created_at=datetime.now(timezone.utc),
            updated_at=datetime.now(timezone.utc),
        )
        test_db.add(other_user)
        test_db.commit()

        # Create instance for first user
        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="User 1 agent message",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # Try to create user message with other user - should fail
        with pytest.raises(ValueError, match="Agent instance not found"):
            create_user_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Unauthorized message",
                user_id=str(other_user.id),
                mark_as_read=True,
            )

    @pytest.mark.integration
    def test_create_user_message_invalid_instance(self, test_db, test_user):
        """Test creating a user message with invalid instance ID."""
        fake_instance_id = str(uuid4())

        # Try to create message for non-existent instance
        with pytest.raises(ValueError, match="Agent instance not found"):
            create_user_message(
                db=test_db,
                agent_instance_id=fake_instance_id,
                content="Message to nowhere",
                user_id=str(test_user.id),
                mark_as_read=True,
            )

    @pytest.mark.integration
    def test_user_messages_in_polling(self, test_db, test_user):
        """Test that user messages created with mark_as_read=False appear in polling."""
        # Create an agent instance with a question
        instance_id, question_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="What should I do?",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=True,
            )
        )
        test_db.commit()

        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        last_read_before = instance.last_read_message_id

        # Create multiple user messages with mark_as_read=False
        msg_ids = []
        for i in range(3):
            result = create_user_message(
                db=test_db,
                agent_instance_id=instance_id,
                content=f"User message {i + 1}",
                user_id=str(test_user.id),
                mark_as_read=False,
            )
            msg_ids.append(result["id"])
        test_db.commit()

        # Verify last_read_message_id hasn't changed
        test_db.refresh(instance)
        assert instance.last_read_message_id == last_read_before

        # Get queued messages - should see all 3
        queued = get_queued_user_messages(
            db=test_db, instance_id=UUID(instance_id), last_read_message_id=None
        )
        assert queued is not None
        assert len(queued) == 3
        assert [msg.content for msg in queued] == [
            "User message 1",
            "User message 2",
            "User message 3",
        ]

        # Now create one with mark_as_read=True
        result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="This one is read",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        read_msg_id = result["id"]
        test_db.commit()

        # Verify last_read_message_id is now the read message
        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(read_msg_id)

        # Get queued messages again - should be empty now
        queued = get_queued_user_messages(
            db=test_db, instance_id=UUID(instance_id), last_read_message_id=None
        )
        assert queued is not None
        assert len(queued) == 0

    @pytest.mark.integration
    def test_interleaved_messages_with_mixed_mark_as_read(self, test_db, test_user):
        """Test interleaved agent and user messages with mixed mark_as_read settings."""
        # Create initial agent instance
        instance_id, msg1_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Agent: Starting work",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # User message 1 - mark as read
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User: Great, proceed",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        test_db.commit()

        # Agent message 2
        _, msg2_id, queued = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Agent: Working on step 1",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()
        assert (
            len(queued) == 0
        )  # No queued messages since user message was marked as read

        # User message 2 - NOT marked as read
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User: Actually, change approach",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        test_db.commit()

        # User message 3 - also NOT marked as read
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User: Use async pattern instead",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        test_db.commit()

        # Agent message 3 - should see the two unread user messages
        _, msg3_id, queued = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Agent: Switching to async pattern",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()
        assert len(queued) == 2
        assert [msg.content for msg in queued] == [
            "User: Actually, change approach",
            "User: Use async pattern instead",
        ]

        # Verify the instance's last_read_message_id is now the agent's last message
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.last_read_message_id == UUID(msg3_id)

        # All messages should be in the database
        all_messages = (
            test_db.query(Message)
            .filter_by(agent_instance_id=instance_id)
            .order_by(Message.created_at)
            .all()
        )
        assert len(all_messages) == 6

        # Verify order and content
        expected_sequence = [
            ("Agent: Starting work", SenderType.AGENT),
            ("User: Great, proceed", SenderType.USER),
            ("Agent: Working on step 1", SenderType.AGENT),
            ("User: Actually, change approach", SenderType.USER),
            ("User: Use async pattern instead", SenderType.USER),
            ("Agent: Switching to async pattern", SenderType.AGENT),
        ]

        for i, (content, sender_type) in enumerate(expected_sequence):
            assert all_messages[i].content == content
            assert all_messages[i].sender_type == sender_type

    @pytest.mark.integration
    @pytest.mark.asyncio
    async def test_complex_conversation_flow_with_questions(self, test_db, test_user):
        """Test a complex conversation with questions, user responses, and mixed read states."""
        # Agent starts and asks a question
        instance_id, q1_id, _ = await send_agent_message(
            db=test_db,
            agent_instance_id=str(uuid4()),
            content="What framework should I use?",
            user_id=str(test_user.id),
            agent_type="Test Agent",
            requires_user_input=True,
        )
        test_db.commit()

        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.status == AgentStatus.AWAITING_INPUT
        assert instance.last_read_message_id == UUID(q1_id)

        # User responds but doesn't mark as read
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Use FastAPI",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        test_db.commit()

        # User adds more context (also not marked as read)
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="And include Pydantic for validation",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        test_db.commit()

        # Agent continues and gets both messages
        _, msg2_id, queued = await send_agent_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Setting up FastAPI with Pydantic",
            user_id=str(test_user.id),
            requires_user_input=False,
        )
        test_db.commit()

        assert len(queued) == 2
        assert [msg.content for msg in queued] == [
            "Use FastAPI",
            "And include Pydantic for validation",
        ]

        # Status should be back to ACTIVE
        test_db.refresh(instance)
        assert instance.status == AgentStatus.ACTIVE

        # Agent asks another question
        _, q2_id, _ = await send_agent_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Should I add authentication?",
            user_id=str(test_user.id),
            requires_user_input=True,
        )
        test_db.commit()

        # User responds and marks as read this time
        create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Yes, use JWT",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        test_db.commit()

        # Agent continues - should not see the already-read message
        _, msg3_id, queued = await send_agent_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Adding JWT authentication",
            user_id=str(test_user.id),
            requires_user_input=False,
        )
        test_db.commit()

        assert len(queued) == 0  # No queued messages since it was marked as read

        # Verify final state
        test_db.refresh(instance)
        assert instance.status == AgentStatus.ACTIVE
        assert instance.last_read_message_id == UUID(msg3_id)

    @pytest.mark.integration
    def test_last_read_tracking_with_multiple_user_messages(self, test_db, test_user):
        """Test that last_read_message_id correctly tracks through multiple user messages."""
        # Create agent instance
        instance_id, msg1_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Agent message 1",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.last_read_message_id == UUID(msg1_id)

        # Add 3 user messages without marking as read
        unread_ids = []
        for i in range(3):
            result = create_user_message(
                db=test_db,
                agent_instance_id=instance_id,
                content=f"Unread user message {i + 1}",
                user_id=str(test_user.id),
                mark_as_read=False,
            )
            unread_ids.append(result["id"])
        test_db.commit()

        # Verify last_read hasn't changed
        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(msg1_id)

        # Add a user message WITH mark as read
        result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="This updates last read",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        read_msg_id = result["id"]
        test_db.commit()

        # Now last_read should jump to this message
        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(read_msg_id)

        # Agent sends next message - should not see any queued messages
        _, agent_msg_id, queued = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=instance_id,
                content="Agent continues",
                user_id=str(test_user.id),
                requires_user_input=False,
            )
        )
        test_db.commit()

        assert len(queued) == 0
        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(agent_msg_id)

    @pytest.mark.integration
    def test_polling_endpoint_simulation_with_interleaved_messages(
        self, test_db, test_user
    ):
        """Simulate the polling endpoint behavior with interleaved messages."""
        # Create instance
        instance_id, msg1_id, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Agent starts",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # User sends message (not marked as read)
        user_msg1_result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User message 1",
            user_id=str(test_user.id),
            mark_as_read=False,
        )
        user_msg1_id = user_msg1_result["id"]
        test_db.commit()

        # Simulate polling endpoint - should see the message
        messages = get_queued_user_messages(
            db=test_db,
            instance_id=UUID(instance_id),
            last_read_message_id=UUID(msg1_id),
        )
        assert messages is not None
        assert len(messages) == 1
        assert messages[0].content == "User message 1"
        test_db.commit()  # Commit the updated last_read

        # Check that last_read was updated by the polling
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.last_read_message_id == UUID(user_msg1_id)

        # User sends another message (marked as read)
        user_msg2_result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="User message 2",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        user_msg2_id = user_msg2_result["id"]
        test_db.commit()

        # Poll again - should return None (stale) because last_read changed
        messages = get_queued_user_messages(
            db=test_db,
            instance_id=UUID(instance_id),
            last_read_message_id=UUID(user_msg1_id),  # Using old ID
        )
        assert messages is None  # Stale!

        # Poll with correct last_read - should see no messages
        messages = get_queued_user_messages(
            db=test_db,
            instance_id=UUID(instance_id),
            last_read_message_id=UUID(user_msg2_id),
        )
        assert messages is not None
        assert len(messages) == 0

    @pytest.mark.integration
    def test_user_message_to_completed_instance_updates_last_read(
        self, test_db, test_user
    ):
        """Test that user messages to completed instances still update last_read_message_id."""
        # Create and complete an instance
        instance_id, _, _ = asyncio.run(
            send_agent_message(
                db=test_db,
                agent_instance_id=str(uuid4()),
                content="Starting task",
                user_id=str(test_user.id),
                agent_type="Test Agent",
                requires_user_input=False,
            )
        )
        test_db.commit()

        # End the session
        end_session(
            db=test_db,
            agent_instance_id=instance_id,
            user_id=str(test_user.id),
        )
        test_db.commit()

        # Verify it's completed
        instance = test_db.query(AgentInstance).filter_by(id=instance_id).first()
        assert instance.status == AgentStatus.COMPLETED
        initial_last_read = instance.last_read_message_id

        # Send user message with mark_as_read=True
        result = create_user_message(
            db=test_db,
            agent_instance_id=instance_id,
            content="Follow-up message to completed instance",
            user_id=str(test_user.id),
            mark_as_read=True,
        )
        msg_id = result["id"]
        test_db.commit()

        # Verify message was created and last_read was updated
        message = test_db.query(Message).filter_by(id=msg_id).first()
        assert message is not None
        assert message.content == "Follow-up message to completed instance"

        test_db.refresh(instance)
        assert instance.last_read_message_id == UUID(msg_id)
        assert instance.last_read_message_id != initial_last_read
