from enum import Enum


class AgentStatus(str, Enum):
    ACTIVE = "ACTIVE"
    AWAITING_INPUT = "AWAITING_INPUT"
    PAUSED = "PAUSED"
    STALE = "STALE"
    COMPLETED = "COMPLETED"
    FAILED = "FAILED"
    KILLED = "KILLED"
    DISCONNECTED = "DISCONNECTED"
    DELETED = "DELETED"


class SenderType(str, Enum):
    AGENT = "AGENT"
    USER = "USER"


class WebhookType(str, Enum):
    """Supported webhook integration types"""

    DEFAULT = "DEFAULT"
    GITHUB = "GITHUB"
