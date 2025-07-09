"""Main client for interacting with the Omnara Agent Dashboard API."""

import time
from typing import Optional, Dict, Any
from urllib.parse import urljoin

import requests
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

from .exceptions import AuthenticationError, TimeoutError, APIError
from .models import (
    LogStepResponse,
    QuestionResponse,
    QuestionStatus,
    EndSessionResponse,
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

        # Set up session with retries
        self.session = requests.Session()
        retry_strategy = Retry(
            total=3, backoff_factor=0.3, status_forcelist=[500, 502, 503, 504]
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
        timeout: Optional[int] = None,
    ) -> Dict[str, Any]:
        """Make an HTTP request to the API.

        Args:
            method: HTTP method (GET, POST, etc.)
            endpoint: API endpoint path
            json: JSON body for the request
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
                method=method, url=url, json=json, timeout=timeout
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

    def log_step(
        self,
        agent_type: str,
        step_description: str,
        agent_instance_id: Optional[str] = None,
    ) -> LogStepResponse:
        """Log a high-level step the agent is performing.

        Args:
            agent_type: Type of agent (e.g., 'Claude Code', 'Cursor')
            step_description: Clear description of what the agent is doing
            agent_instance_id: Existing agent instance ID (optional)

        Returns:
            LogStepResponse with success status, instance ID, and user feedback
        """
        data = {"agent_type": agent_type, "step_description": step_description}
        if agent_instance_id:
            data["agent_instance_id"] = agent_instance_id

        response = self._make_request("POST", "/api/v1/steps", json=data)

        return LogStepResponse(
            success=response["success"],
            agent_instance_id=response["agent_instance_id"],
            step_number=response["step_number"],
            user_feedback=response.get("user_feedback", []),
        )

    def ask_question(
        self,
        agent_instance_id: str,
        question_text: str,
        timeout_minutes: int = 1440,
        poll_interval: float = 1.0,
    ) -> QuestionResponse:
        """Ask the user a question and wait for their response.

        This method submits the question and then polls for the answer.

        Args:
            agent_instance_id: Agent instance ID
            question_text: Question to ask the user
            timeout_minutes: Maximum time to wait for answer in minutes (default: 1440 = 24 hours)
            poll_interval: Time between polls in seconds (default: 1.0)

        Returns:
            QuestionResponse with the user's answer

        Raises:
            TimeoutError: If no answer is received within timeout
        """
        # Submit the question
        data = {"agent_instance_id": agent_instance_id, "question_text": question_text}

        # First, try the non-blocking endpoint to create the question
        response = self._make_request("POST", "/api/v1/questions", json=data, timeout=5)
        question_id = response["question_id"]

        # Convert timeout from minutes to seconds
        timeout_seconds = timeout_minutes * 60

        # Poll for the answer
        start_time = time.time()
        while time.time() - start_time < timeout_seconds:
            status = self.get_question_status(question_id)

            if status.status == "answered" and status.answer:
                return QuestionResponse(answer=status.answer, question_id=question_id)

            time.sleep(poll_interval)

        raise TimeoutError(f"Question timed out after {timeout_minutes} minutes")

    def get_question_status(self, question_id: str) -> QuestionStatus:
        """Get the current status of a question.

        Args:
            question_id: ID of the question to check

        Returns:
            QuestionStatus with current status and answer (if available)
        """
        response = self._make_request("GET", f"/api/v1/questions/{question_id}")

        return QuestionStatus(
            question_id=response["question_id"],
            status=response["status"],
            answer=response.get("answer"),
            asked_at=response["asked_at"],
            answered_at=response.get("answered_at"),
        )

    def end_session(self, agent_instance_id: str) -> EndSessionResponse:
        """End an agent session and mark it as completed.

        Args:
            agent_instance_id: Agent instance ID to end

        Returns:
            EndSessionResponse with success status and final details
        """
        data = {"agent_instance_id": agent_instance_id}
        response = self._make_request("POST", "/api/v1/sessions/end", json=data)

        return EndSessionResponse(
            success=response["success"],
            agent_instance_id=response["agent_instance_id"],
            final_status=response["final_status"],
        )

    def close(self):
        """Close the session and clean up resources."""
        self.session.close()
