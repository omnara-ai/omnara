"""Framing helpers for terminal relay communication."""

from __future__ import annotations

import struct
from typing import Iterator, Tuple

FRAME_HEADER = struct.Struct("!BI")
FRAME_TYPE_OUTPUT = 0
FRAME_TYPE_INPUT = 1
FRAME_TYPE_RESIZE = 2


def pack_frame(frame_type: int, payload: bytes) -> bytes:
    """Serialize a frame with a simple type + length header."""

    return FRAME_HEADER.pack(frame_type, len(payload)) + payload


def iter_frames(buffer: bytearray) -> Iterator[Tuple[int, bytes]]:
    """Yield complete frames from a mutable buffer."""

    while True:
        if len(buffer) < FRAME_HEADER.size:
            return

        frame_type, frame_len = FRAME_HEADER.unpack(buffer[: FRAME_HEADER.size])
        total_len = FRAME_HEADER.size + frame_len
        if len(buffer) < total_len:
            return

        payload = bytes(buffer[FRAME_HEADER.size : total_len])
        del buffer[:total_len]
        yield frame_type, payload
