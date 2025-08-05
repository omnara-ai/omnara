"""Main client for interacting with the Omnara Agent Dashboard API."""

import time
import uuid
from typing import Optional, Dict, Any, Union, List
from urllib.parse import urljoin

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

from .exceptions import AuthenticationError, TimeoutError, APIError
from .models import (
    EndSessionResponse,
    CreateMessageResponse,
    PendingMessagesResponse,
    Message,
)
from .utils import (
    validate_agent_instance_id,
    build_message_request_data,
)


class OmnaraClient:
    """Client for interacting with the Omnara Agent Dashboard API.

    Args:
        api_key: JWT API key for authentication
        base_url: Base URL of the API server (default: https://agent-dashboard-mcp.onrender.com)
        timeout: Default timeout for requests in seconds (default: 30)
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = "https://agent-dashboard-mcp.onrender.com",
        timeout: int = 30,
    ):
        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout

        # Set up session with urllib3 retry strategy
        self.session = requests.Session()

        # Configure retry strategy
        retry_strategy = Retry(
            total=5,  # Total number of retries
            backoff_factor=1.0,  # Exponential backoff: 1s, 2s, 4s, 8s, 16s
            status_forcelist=[429, 500, 502, 503, 504],  # Retry on these HTTP codes
            allowed_methods=["GET", "POST", "PUT", "DELETE", "PATCH"],
            raise_on_status=False,
            # Important: retry on connection errors
            connect=5,  # Number of connection-related errors to retry
            read=5,  # Number of read errors to retry
            other=5,  # Number of other errors to retry
        )
        adapter = HTTPAdapter(max_retries=retry_strategy)
        self.session.mount("http://", adapter)
        self.session.mount("https://", adapter)

        # Set default headers
        self.session.headers.update(
            {"Authorization": f"Bearer {api_key}", "Content-Type": "application/json"}
        )

    def __enter__(self):
        """Context manager entry."""
        return self

    def __exit__(self, exc_type, exc_val, exc_tb):
        """Context manager exit."""
        self.close()

    def _make_request(
        self,
        method: str,
        endpoint: str,
        json: Optional[Dict[str, Any]] = None,
        params: Optional[Dict[str, Any]] = None,
        timeout: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Make an HTTP request to the API.

        Args:
            method: HTTP method (GET, POST, etc.)
            endpoint: API endpoint path
            json: JSON body for the request
            params: Query parameters for the request
            timeout: Request timeout in seconds

        Returns:
            Response JSON data

        Raises:
            AuthenticationError: If authentication fails
            APIError: If the API returns an error
            TimeoutError: If the request times out
        """
        url = urljoin(self.base_url, endpoint)
        timeout = timeout or self.timeout

        try:
            response = self.session.request(
                method=method, url=url, json=json, params=params, timeout=timeout
            )

            if response.status_code == 401:
                raise AuthenticationError("Invalid API key or authentication failed")

            if not response.ok:
                try:
                    error_detail = response.json().get("detail", response.text)
                except Exception:
                    error_detail = response.text
                raise APIError(response.status_code, error_detail)

            return response.json()

        except requests.exceptions.Timeout:
            raise TimeoutError(f"Request timed out after {timeout} seconds")
        except requests.exceptions.RequestException as e:
            raise APIError(0, f"Request failed: {str(e)}")

    def send_message(
        self,
        content: str,
        agent_type: Optional[str] = None,
        agent_instance_id: Optional[Union[str, uuid.UUID]] = None,
        requires_user_input: bool = False,
        timeout_minutes: int = 1440,
        poll_interval: float = 10.0,
        send_push: Optional[bool] = None,
        send_email: Optional[bool] = None,
        send_sms: Optional[bool] = None,
        git_diff: Optional[str] = None,
    ) -> CreateMessageResponse:
        """Send a message to the dashboard.

        Args:
            content: The message content (step description or question text)
            agent_type: Type of agent (required if agent_instance_id not provided)
            agent_instance_id: Existing agent instance ID (optional)
            requires_user_input: Whether this message requires user input (default: False)
            timeout_minutes: If requires_user_input, max time to wait in minutes (default: 1440)
            poll_interval: If requires_user_input, time between polls in seconds (default: 10.0)
            send_push: Send push notification (default: False for steps, user pref for questions)
            send_email: Send email notification (default: False for steps, user pref for questions)
            send_sms: Send SMS notification (default: False for steps, user pref for questions)
            git_diff: Git diff content to include (optional)

        Returns:
            CreateMessageResponse with any queued user messages

        Raises:
            ValueError: If neither agent_type nor agent_instance_id is provided
            TimeoutError: If requires_user_input and no answer is received within timeout
        """
        # If no agent_instance_id provided, generate one client-side
        if not agent_instance_id:
            if not agent_type:
                raise ValueError("agent_type is required when creating a new instance")
            agent_instance_id = uuid.uuid4()

        # Validate and convert agent_instance_id to string
        agent_instance_id_str = validate_agent_instance_id(agent_instance_id)

        # Build request data using shared utility
        data = build_message_request_data(
            content=content,
            agent_instance_id=agent_instance_id_str,
            requires_user_input=requires_user_input,
            agent_type=agent_type,
            send_push=send_push,
            send_email=send_email,
            send_sms=send_sms,
            git_diff=git_diff,
        )

        # Send the message
        response = self._make_request("POST", "/api/v1/messages/agent", json=data)
        response_agent_instance_id = response["agent_instance_id"]
        message_id = response["message_id"]

        queued_contents = [
            msg["content"] if isinstance(msg, dict) else msg
            for msg in response.get("queued_user_messages", [])
        ]

        create_response = CreateMessageResponse(
            success=response["success"],
            agent_instance_id=response_agent_instance_id,
            message_id=message_id,
            queued_user_messages=queued_contents,
        )

        # If it doesn't require user input, return immediately with any queued messages
        if not requires_user_input:
            return create_response

        # Otherwise, we need to poll for user response
        # Use the message ID we just created as our starting point
        last_read_message_id = message_id

        timeout_seconds = timeout_minutes * 60
        start_time = time.time()
        all_messages = []

        while time.time() - start_time < timeout_seconds:
            # Poll for pending messages
            pending_response = self.get_pending_messages(
                agent_instance_id_str, last_read_message_id
            )

            # If status is "stale", another process has read the messages
            if pending_response.status == "stale":
                raise TimeoutError("Another process has read the messages")

            # Check if we got any messages
            if pending_response.messages:
                # Collect all messages
                all_messages.extend(pending_response.messages)

                # Return the response with all collected messages
                create_response.queued_user_messages = [
                    msg.content for msg in all_messages
                ]
                return create_response

            time.sleep(poll_interval)

        raise TimeoutError(f"Question timed out after {timeout_minutes} minutes")

    def get_pending_messages(
        self,
        agent_instance_id: Union[str, uuid.UUID],
        last_read_message_id: Optional[str] = None,
    ) -> PendingMessagesResponse:
        """Get pending user messages for an agent instance.

        Args:
            agent_instance_id: Agent instance ID
            last_read_message_id: The last message ID that was read (optional)

        Returns:
            PendingMessagesResponse with messages and status
        """
        # Validate and convert agent_instance_id to string
        agent_instance_id_str = validate_agent_instance_id(agent_instance_id)

        params = {"agent_instance_id": agent_instance_id_str}
        if last_read_message_id:
            params["last_read_message_id"] = last_read_message_id

        response = self._make_request("GET", "/api/v1/messages/pending", params=params)

        return PendingMessagesResponse(
            agent_instance_id=response["agent_instance_id"],
            messages=[Message(**msg) for msg in response["messages"]],
            status=response["status"],
        )

    def send_user_message(
        self,
        agent_instance_id: Union[str, uuid.UUID],
        content: str,
        mark_as_read: bool = True,
    ) -> Dict[str, Any]:
        """Send a user message to an agent instance.

        Args:
            agent_instance_id: The agent instance ID to send the message to
            content: Message content
            mark_as_read: Whether to mark as read (update last_read_message_id) (default: True)

        Returns:
            Dict containing:
                - success: Whether the message was created
                - message_id: ID of the created message
                - marked_as_read: Whether the message was marked as read

        Raises:
            ValueError: If agent instance not found or access denied
            APIError: If the API request fails
        """
        # Validate and convert agent_instance_id
        agent_instance_id = validate_agent_instance_id(agent_instance_id)

        data = {
            "agent_instance_id": str(agent_instance_id),
            "content": content,
            "mark_as_read": mark_as_read,
        }

        return self._make_request("POST", "/api/v1/messages/user", json=data)

    def request_user_input(
        self,
        message_id: Union[str, uuid.UUID],
        timeout_minutes: int = 1440,
        poll_interval: float = 10.0,
    ) -> List[str]:
        """Request user input for a previously sent agent message.

        This method updates an agent message to require user input and polls for responses.
        It's useful when you initially send a message without requiring input, but later
        decide you need user feedback.

        Args:
            message_id: The message ID to update (must be an agent message)
            timeout_minutes: Max time to wait for user response in minutes (default: 1440)
            poll_interval: Time between polls in seconds (default: 10.0)

        Returns:
            List of user message contents received as responses

        Raises:
            ValueError: If message not found, already requires input, or not an agent message
            TimeoutError: If no user response is received within timeout
            APIError: If the API request fails
        """
        # Convert message_id to string if it's a UUID
        message_id_str = str(message_id)

        # Call the endpoint to update the message
        response = self._make_request(
            "PATCH", f"/api/v1/messages/{message_id_str}/request-input"
        )

        agent_instance_id = response["agent_instance_id"]
        messages = response.get("messages", [])

        if messages:
            return [msg["content"] for msg in messages]

        # Otherwise, poll for user response
        timeout_seconds = timeout_minutes * 60
        start_time = time.time()
        all_messages = []

        while time.time() - start_time < timeout_seconds:
            # Poll for pending messages using the message_id as last_read
            pending_response = self.get_pending_messages(
                agent_instance_id, message_id_str
            )

            # If status is "stale", another process has read the messages
            if pending_response.status == "stale":
                raise TimeoutError("Another process has read the messages")

            # Check if we got any messages
            if pending_response.messages:
                # Collect all message contents
                all_messages.extend([msg.content for msg in pending_response.messages])
                return all_messages

            time.sleep(poll_interval)

        raise TimeoutError(f"No user response received after {timeout_minutes} minutes")

    def end_session(
        self, agent_instance_id: Union[str, uuid.UUID]
    ) -> EndSessionResponse:
        """End an agent session and mark it as completed.

        Args:
            agent_instance_id: Agent instance ID to end

        Returns:
            EndSessionResponse with success status and final details
        """
        # Validate and convert agent_instance_id to string
        agent_instance_id_str = validate_agent_instance_id(agent_instance_id)

        data: Dict[str, Any] = {"agent_instance_id": agent_instance_id_str}
        response = self._make_request("POST", "/api/v1/sessions/end", json=data)

        return EndSessionResponse(
            success=response["success"],
            agent_instance_id=response["agent_instance_id"],
            final_status=response["final_status"],
        )

    def close(self):
        """Close the session and clean up resources."""
        self.session.close()
