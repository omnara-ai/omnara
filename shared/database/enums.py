from enum import Enum


class AgentStatus(str, Enum):
    ACTIVE = "active"
    AWAITING_INPUT = "awaiting_input"
    PAUSED = "paused"
    STALE = "stale"
    COMPLETED = "completed"
    FAILED = "failed"
    KILLED = "killed"
    DISCONNECTED = "disconnected"


class SenderType(str, Enum):
    AGENT = "agent"
    USER = "user"
