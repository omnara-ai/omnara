"""Abstract base class for notification services"""

from abc import ABC, abstractmethod
from typing import Dict, Union
from uuid import UUID
from sqlalchemy.orm import Session


class NotificationServiceBase(ABC):
    """Abstract base class defining the interface for notification services"""

    @abstractmethod
    def send_notification(
        self, db: Session, user_id: UUID, title: str, body: str, **kwargs
    ) -> Union[bool, Dict[str, bool]]:
        """Send a general notification

        Returns:
            bool for single-channel services (push)
            Dict[str, bool] for multi-channel services (email/SMS)
        """
        pass

    @abstractmethod
    def send_question_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        question_text: str,
        **kwargs,
    ) -> Union[bool, Dict[str, bool]]:
        """Send notification for a new agent question"""
        pass

    @abstractmethod
    async def send_step_notification(
        self,
        db: Session,
        user_id: UUID,
        instance_id: str,
        agent_name: str,
        step_description: str,
        **kwargs,
    ) -> Union[bool, Dict[str, bool]]:
        """Send notification for a new agent step"""
        pass
