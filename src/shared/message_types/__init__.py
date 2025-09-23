"""Shared message metadata schemas and helpers.

These models define the structured payloads agents can attach to messages
via the existing `message_metadata` JSONB column. The goal is to keep
metadata flexible (plain dict storage) while still providing validation
helpers that agents and backend code can use.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Iterable, Mapping, MutableMapping, Sequence, Type

from pydantic import BaseModel, ConfigDict, Field, TypeAdapter, ValidationError


class MessageMetadataError(ValueError):
    """Raised when message metadata fails validation."""


class CodeEditChange(BaseModel):
    """A single file change in a code-edit payload."""

    file_path: str = Field(..., min_length=1)
    old_content: str | None = None
    new_content: str | None = None
    language: str | None = None

    model_config = ConfigDict(extra="forbid")


class CodeEditPayload(BaseModel):
    """Payload describing one or more file edits."""

    edits: list[CodeEditChange] = Field(..., min_length=1)

    model_config = ConfigDict(extra="forbid")


class TodoItemPayload(BaseModel):
    """A single todo item."""

    id: str = Field(..., min_length=1)
    content: str = Field(..., min_length=1)
    status: str = Field(..., pattern=r"^(pending|in_progress|completed)$")

    model_config = ConfigDict(extra="forbid")


class TodoPayload(BaseModel):
    """Payload containing a list of todo items."""

    todos: list[TodoItemPayload] = Field(..., min_length=1)

    model_config = ConfigDict(extra="forbid")


class ToolCallPayload(BaseModel):
    """Payload describing a tool invocation."""

    tool_name: str = Field(..., min_length=1)
    input: dict[str, Any] = Field(default_factory=dict)
    output: Any | None = None
    error: str | None = None

    model_config = ConfigDict(extra="allow")


MessagePayload = CodeEditPayload | TodoPayload | ToolCallPayload


class MessageMetadataEnvelope(BaseModel):
    """Envelope containing the payload and bookkeeping fields."""

    message_type: str = Field(
        ..., min_length=1, description="Discriminator for payload type"
    )
    version: int | None = Field(
        default=1,
        ge=1,
        description="Schema version for the payload, reserved for future use",
    )
    payload: dict[str, Any] = Field(
        default_factory=dict,
        description="Type-specific payload data",
    )

    model_config = ConfigDict(extra="allow")


KNOWN_PAYLOADS: dict[str, Type[MessagePayload]] = {
    "CODE_EDIT": CodeEditPayload,
    "TODO": TodoPayload,
    "TOOL_CALL": ToolCallPayload,
}

_PAYLOAD_ADAPTERS: dict[str, TypeAdapter[Any]] = {
    message_type: TypeAdapter(model) for message_type, model in KNOWN_PAYLOADS.items()
}


@dataclass(frozen=True)
class ParsedMessageMetadata:
    """Result of parsing a metadata blob."""

    raw: dict[str, Any] | None
    message_type: str | None
    payload: MessagePayload | dict[str, Any] | None
    is_legacy: bool

    @property
    def has_structured_payload(self) -> bool:
        return isinstance(self.payload, BaseModel)

    @property
    def kind(self) -> str | None:
        """Compatibility alias for callers still expecting ``kind``."""

        return self.message_type


def normalize_message_metadata(
    metadata: Mapping[str, Any] | MutableMapping[str, Any] | None,
) -> dict[str, Any] | None:
    """Validate and normalize the provided metadata for persistence.

    Unknown message types are allowed (the payload is left as-is) so that third-party
    agents can experiment without waiting for a backend rollout. Known types
    are validated against their Pydantic models and re-serialized to ensure
    consistent shapes. Legacy metadata (lacking a ``message_type`` field) is returned
    untouched to preserve backward compatibility.
    """

    if metadata is None:
        return None

    if not isinstance(metadata, Mapping):
        raise MessageMetadataError("Message metadata must be a JSON object")

    if "message_type" not in metadata:
        return dict(metadata)

    try:
        envelope = MessageMetadataEnvelope.model_validate(metadata)
    except ValidationError as exc:  # pragma: no cover - exercised via callers
        raise MessageMetadataError("Invalid message metadata envelope") from exc

    adapter = _PAYLOAD_ADAPTERS.get(envelope.message_type)
    payload_dump: dict[str, Any]

    if adapter is not None:
        try:
            payload_model = adapter.validate_python(envelope.payload)
        except ValidationError as exc:  # pragma: no cover - exercised via callers
            raise MessageMetadataError("Invalid payload for message metadata") from exc

        if isinstance(payload_model, BaseModel):
            payload_dump = payload_model.model_dump(exclude_none=True)
        elif isinstance(payload_model, Mapping):
            payload_dump = dict(payload_model)
        else:  # pragma: no cover - defensive (TypeAdapter should return BaseModel)
            payload_dump = dict(envelope.payload)
    else:
        payload_dump = dict(envelope.payload)

    normalized = envelope.model_dump(exclude_none=True)
    normalized["payload"] = payload_dump
    return normalized


def parse_message_metadata(metadata: Mapping[str, Any] | None) -> ParsedMessageMetadata:
    """Parse metadata into an envelope + typed payload when possible."""

    if metadata is None:
        return ParsedMessageMetadata(
            raw=None, message_type=None, payload=None, is_legacy=False
        )

    if not isinstance(metadata, Mapping):
        raise MessageMetadataError("Message metadata must be a JSON object")

    if "message_type" not in metadata:
        return ParsedMessageMetadata(
            raw=dict(metadata),
            message_type=None,
            payload=dict(metadata),
            is_legacy=True,
        )

    envelope = MessageMetadataEnvelope.model_validate(metadata)
    adapter = _PAYLOAD_ADAPTERS.get(envelope.message_type)

    payload: MessagePayload | dict[str, Any]
    if adapter is not None:
        payload = adapter.validate_python(envelope.payload)
    else:
        payload = dict(envelope.payload)

    normalized = envelope.model_dump(exclude_none=True)
    if isinstance(payload, BaseModel):
        normalized["payload"] = payload.model_dump(exclude_none=True)
    else:
        normalized["payload"] = payload

    return ParsedMessageMetadata(
        raw=normalized,
        message_type=envelope.message_type,
        payload=payload,
        is_legacy=False,
    )


def iter_known_message_types() -> Iterable[str]:
    """Return the message types that have first-class payload models."""

    return KNOWN_PAYLOADS.keys()


def build_structured_envelope(
    *,
    message_type: str,
    payload: Mapping[str, Any] | BaseModel | None = None,
    version: int = 1,
    extras: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Build a structured metadata envelope for the provided payload."""

    if not message_type:
        raise MessageMetadataError(
            "message_type is required for structured message metadata"
        )

    if payload is None:
        payload_dict: dict[str, Any] = {}
    elif isinstance(payload, BaseModel):
        payload_dict = payload.model_dump(exclude_none=True)
    elif isinstance(payload, Mapping):
        payload_dict = {k: v for k, v in payload.items() if v is not None}
    else:
        raise MessageMetadataError("payload must be a mapping or BaseModel instance")

    envelope: dict[str, Any] = {
        "message_type": message_type,
        "version": version,
        "payload": payload_dict,
    }

    if extras:
        for key in extras:
            if key in {"message_type", "version", "payload"}:
                raise MessageMetadataError(
                    f"extras cannot override envelope field '{key}'"
                )
        envelope.update(dict(extras))

    normalized = normalize_message_metadata(envelope)
    return normalized if normalized is not None else envelope


def build_code_edit_envelope(
    edits: Sequence[CodeEditChange | dict[str, Any]],
    *,
    version: int = 1,
    extras: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Create a CODE_EDIT envelope from edits.

    Accepts either ``CodeEditChange`` instances or plain dictionaries that supply
    the required fields.
    """

    if not edits:
        raise MessageMetadataError("Code edit messages require at least one edit")

    serialized: list[dict[str, Any]] = []
    for edit in edits:
        if isinstance(edit, CodeEditChange):
            serialized.append(edit.model_dump(exclude_none=True))
        elif isinstance(edit, dict):
            serialized.append({k: v for k, v in edit.items() if v is not None})
        else:
            raise MessageMetadataError(
                "Code edits must be CodeEditChange instances or dictionaries"
            )

    payload = {"edits": serialized}
    return build_structured_envelope(
        message_type="CODE_EDIT",
        payload=payload,
        version=version,
        extras=extras,
    )


def build_todo_envelope(
    todos: Sequence[TodoItemPayload | dict[str, Any]],
    *,
    version: int = 1,
    extras: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Create a TODO envelope from todo items.

    Accepts either ``TodoItemPayload`` instances or plain dictionaries.
    """

    if not todos:
        raise MessageMetadataError("Todo messages require at least one todo item")

    serialized: list[dict[str, Any]] = []
    for item in todos:
        if isinstance(item, TodoItemPayload):
            serialized.append(item.model_dump())
        elif isinstance(item, dict):
            serialized.append(dict(item))
        else:
            raise MessageMetadataError(
                "Todo items must be TodoItemPayload instances or dictionaries"
            )

    payload = {"todos": serialized}
    return build_structured_envelope(
        message_type="TODO",
        payload=payload,
        version=version,
        extras=extras,
    )


def build_tool_call_envelope(
    *,
    tool_name: str,
    input: Mapping[str, Any] | None = None,
    output: Any | None = None,
    error: str | None = None,
    version: int = 1,
    extras: Mapping[str, Any] | None = None,
) -> dict[str, Any]:
    """Create a TOOL_CALL envelope."""

    if not tool_name:
        raise MessageMetadataError("tool_name is required for TOOL_CALL metadata")

    payload: dict[str, Any] = {
        "tool_name": tool_name,
        "input": dict(input) if input is not None else {},
    }
    if output is not None:
        payload["output"] = output
    if error is not None:
        payload["error"] = error

    return build_structured_envelope(
        message_type="TOOL_CALL",
        payload=payload,
        version=version,
        extras=extras,
    )


__all__ = [
    "MessageMetadataError",
    "CodeEditChange",
    "CodeEditPayload",
    "TodoItemPayload",
    "TodoPayload",
    "ToolCallPayload",
    "MessagePayload",
    "MessageMetadataEnvelope",
    "normalize_message_metadata",
    "parse_message_metadata",
    "ParsedMessageMetadata",
    "iter_known_message_types",
    "build_structured_envelope",
    "build_code_edit_envelope",
    "build_todo_envelope",
    "build_tool_call_envelope",
]
