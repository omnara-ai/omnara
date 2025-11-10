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


class InstanceAccessLevel(str, Enum):
    READ = "READ"
    WRITE = "WRITE"


class TeamRole(str, Enum):
    OWNER = "OWNER"
    ADMIN = "ADMIN"
    MEMBER = "MEMBER"


class PromptQueueStatus(str, Enum):
    PENDING = "PENDING"
    SENT = "SENT"
    FAILED = "FAILED"
    CANCELLED = "CANCELLED"
