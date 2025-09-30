#!/usr/bin/env python3
"""
Gemini CLI Wrapper for Omnara (minimal v1)

This mirrors the Claude/Amp PTY-based approach:
- Finds and launches the `gemini` CLI in a PTY
- Forwards stdin to the child and captures stdout/stderr
- Sends user/assistant messages to the Omnara dashboard
- Uses simple idle-based segmentation for assistant messages

Environment variables respected:
- OMNARA_API_KEY (required if --api-key omitted)
- OMNARA_API_URL (optional; defaults to https://agent.omnara.com)
- OMNARA_SESSION_ID (optional; auto-generated if not set)
- OMNARA_GEMINI_PATH (optional; path/dir to gemini binary override)
"""

import argparse
import os
import pty
import select
import shutil
import signal
import atexit
import sys
import termios
import threading
import time
import tty
import uuid
from pathlib import Path
from typing import Optional

from omnara.sdk.client import OmnaraClient
from integrations.utils.git_utils import GitDiffTracker
import subprocess


# Basic ANSI escape code regex compatible with Amp/Claude tests
import re
import struct
import fcntl

ANSI_ESCAPE = re.compile(r"\x1B(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~])")
# Handles the case where ESC was stripped upstream, leaving bracket codes like "[38;2;R;G;B" or "[38;2;R;G;Bm"
BRACKET_COLOR_CODE = re.compile(r"\[(?:\d{1,3}(?:;\d{1,3})*)m?")
# Permission/confirmation UI detection
CONFIRM_APPLY_RE = re.compile(r"^\s*Apply this change\?\s*$", re.IGNORECASE)
CONFIRM_OPTION_RE = re.compile(
    r"^\s*(?:[\u25CF•]\s*)?(\d+)\.\s*(Yes, allow once|Yes, allow always|Modify with external editor|No, suggest changes)\s*$",
    re.IGNORECASE,
)
QUERY_HEADER_RE = re.compile(r"^\s*\?\s+\w+\b")
BULLET_PREFIX_RE = re.compile(r"^\s*[\u25CF•]\s+")

# Generic, configurable noise filter for whimsical/ephemeral status lines
_DEFAULT_NOISE_RE = re.compile(
    r"(?i)\b(waiting|loading|initializ|prepar|optimiz|warm|think|process|boot|connect|hold on|just a sec|be back)\b.*[…\.]{1,3}\s*$"
)
_EXTRA_NOISE_RES: list[re.Pattern] = []
try:
    _noise_env = os.environ.get("OMNARA_GEMINI_NOISE_REGEX")
    if _noise_env:
        # Allow multiple patterns separated by newlines or ||
        for part in re.split(r"\n|\|\|", _noise_env):
            s = part.strip()
            if not s:
                continue
            try:
                _EXTRA_NOISE_RES.append(re.compile(s, re.IGNORECASE))
            except Exception:
                pass
except Exception:
    pass
CONFIRM_APPLY_RE = re.compile(r"^\s*Apply this change\?\s*$", re.IGNORECASE)
CONFIRM_OPTION_RE = re.compile(
    r"^\s*(?:[\u25CF•]\s*)?(?:[1-9]\.|\*)?\s*(Yes, allow once|Yes, allow always|Modify with external editor|No, suggest changes)",
    re.IGNORECASE,
)
QUERY_HEADER_RE = re.compile(r"^\s*\?\s+\w+\b")
BULLET_PREFIX_RE = re.compile(r"^\s*[\u25CF•]\s+")


def strip_ansi(text: str) -> str:
    try:
        no_esc = ANSI_ESCAPE.sub("", text)
        # Also remove stray bracket color codes if ESC was removed earlier
        no_brackets = BRACKET_COLOR_CODE.sub("", no_esc)
        # Remove leading stray numeric color fragments like "2;" that sometimes remain
        no_frag = re.sub(r"(^|\s)(?:\d{1,3};){1,3}(?=\S)", r"\1", no_brackets)
        return no_frag
    except Exception:
        return text


def strip_leading_user_echo(content: str) -> str:
    """Heuristically remove a leading echoed user prompt from Gemini output.

    Many TUIs render the submitted prompt inline before the assistant reply,
    separated by a decorative bullet/marker (e.g., '✦', '•', '—'). If that
    prompt leaks into the captured assistant text, remove the leading part up
    to and including the first separator when:
      - It appears within the first ~200 characters
      - There is no newline before the separator (single-line header)
      - The prefix contains at least one word character (actual text)
    """
    if not content:
        return content
    # Only examine the first line to avoid stripping legitimate multi-line content
    first_newline = content.find("\n")
    head = content if first_newline == -1 else content[:first_newline]
    # Candidate separators commonly used in TUIs
    seps = set("✦•·—–:|│>-")
    idx = -1
    for i, ch in enumerate(head[:200]):
        if ch in seps:
            idx = i
            break
    if idx <= 0:
        return content
    # Must have at least one word char before the separator to consider it an echo
    if not re.search(r"\w", head[:idx]):
        return content
    # Remove everything up to and including the separator, then trim leading space
    stripped_head = head[idx + 1 :].lstrip()
    tail = content[first_newline + 1 :] if first_newline != -1 else ""
    new_first_line = stripped_head
    return new_first_line + ("\n" + tail if tail else "")


def extract_user_echo_and_rest(content: str) -> tuple[str | None, str]:
    """Return (user_echo, rest) using the same heuristic as strip_leading_user_echo."""
    if not content:
        return None, content
    first_newline = content.find("\n")
    head = content if first_newline == -1 else content[:first_newline]
    seps = set("✦•·—–:|│>-")
    idx = -1
    for i, ch in enumerate(head[:200]):
        if ch in seps:
            idx = i
            break
    if idx <= 0:
        return None, content
    if not re.search(r"\w", head[:idx]):
        return None, content
    user_echo = head[:idx].strip()
    rest_head = head[idx + 1 :].lstrip()
    tail = content[first_newline + 1 :] if first_newline != -1 else ""
    rest = rest_head + ("\n" + tail if tail else "")
    return user_echo or None, rest


def collapse_repeated_segments(text: str) -> str:
    """Collapse consecutive duplicate segments split by common UI separators.

    This reduces redraw-induced repetition like "A✦ A✦ A" to a single "A".
    """
    if not text:
        return text
    parts = re.split(r"[\n✦]+", text)
    out: list[str] = []
    last: str | None = None
    for p in parts:
        t = p.strip()
        if not t:
            continue
        if last is not None and t == last:
            continue
        out.append(t)
        last = t
    return "\n".join(out)


# Note: we intentionally avoid any stripping based solely on the locally
# recorded last input to ensure we only reflect what Gemini actually echoed.


BOX_DRAW_CHARS = set("╭╮╯╰│─┌┐└┘┤├┴┬┼")
# Common braille spinner glyphs used by CLIs
BRAILLE_SPINNER_CHARS = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
BRAILLE_SPINNER_RE = re.compile("[" + re.escape(BRAILLE_SPINNER_CHARS) + "]+")
# Remove any inline segment that contains the cancel hint with a time counter
# Case-insensitive; removes the whole line/segment regardless of surrounding text
ESC_TO_CANCEL_SEGMENT_RE = re.compile(
    r"(?:\s|^)[^\r\n]*\(esc to cancel,\s*\d+s\)[^\r\n]*",
    re.IGNORECASE,
)


def is_mostly_banner(line: str) -> bool:
    # Drop heavy block-art lines
    blocks = sum(line.count(ch) for ch in ("█", "░", "▓", "▒"))
    return len(line) > 0 and blocks / max(len(line), 1) > 0.35


def filter_visual_noise(text: str) -> str:
    """Remove common Gemini CLI banners, spinners, and tips that pollute logs."""
    # Remove braille spinner glyphs globally; they convey no semantic content
    text = BRAILLE_SPINNER_RE.sub("", text)
    # Remove inline spinner/status segments containing "esc to cancel"
    # Apply repeatedly in case multiple segments exist in the same chunk
    for _ in range(3):
        new_text = ESC_TO_CANCEL_SEGMENT_RE.sub("", text)
        if new_text == text:
            break
        text = new_text

    lines: list[str] = []
    seen: set[str] = set()
    for raw in text.splitlines():
        line = raw.strip("\r")
        if not line:
            continue
        # Remove lingering bracket color codes within the line
        line = BRACKET_COLOR_CODE.sub("", line)
        # Remove leftover numeric color fragments like "2;" at start
        line = re.sub(r"^(?:\d{1,3};){1,3}", "", line).strip()
        # Common tip panel and auth wait status
        if "Waiting for auth" in line:
            continue
        if "Tips for getting started" in line:
            continue
        if "Ask questions, edit files" in line:
            continue
        if "/help for more information" in line:
            continue
        if "Gemini - " in line:
            continue
        if "Loaded cached credentials" in line:
            continue
        if "no sandbox (see /docs)" in line:
            continue
        # Drop any mention of "no sandbox" regardless of surrounding text (GitHub Codespaces-type banners)
        if "no sandbox" in line.lower():
            continue
        # Drop known status phrases
        if "Waiting for user confirmation" in line:
            continue
        if "Painting the serifs back on" in line:
            continue
        # Generic noise lines (configurable + heuristic)
        try:
            if _DEFAULT_NOISE_RE.search(line) or any(p.search(line) for p in _EXTRA_NOISE_RES):
                continue
        except Exception:
            pass
        if "context left)" in line:
            continue
        if re.search(r"\bgemini-.*context left\b", line, re.IGNORECASE):
            continue
        # Skip confirmation UI and prompt headers/options
        if CONFIRM_APPLY_RE.match(line) or CONFIRM_OPTION_RE.match(line) or QUERY_HEADER_RE.match(line) or BULLET_PREFIX_RE.match(line):
            continue
        # Skip confirmation UI and prompt headers/options
        if CONFIRM_APPLY_RE.match(line) or CONFIRM_OPTION_RE.match(line) or QUERY_HEADER_RE.match(line) or BULLET_PREFIX_RE.match(line):
            continue
        # Skip common git-branch prompt fragments appearing anywhere in the line
        if "(main*)" in line or re.search(r"\(\w+\*?\)", line):
            continue
        # Skip cwd prompt-style lines like ~/project/path
        if re.match(r"^~?/[\w\-./]+$", line):
            continue
        # Ignore any residual cancel-hint lines (case-insensitive)
        if "(esc to cancel" in line.lower():
            continue
        # Box drawing noise
        if any(ch in line for ch in BOX_DRAW_CHARS):
            continue
        # Heavy banner lines
        if is_mostly_banner(line):
            continue
        # Skip pure color-code artifacts
        if BRACKET_COLOR_CODE.fullmatch(line):
            continue
        # Skip the common Gemini tip lines repeated often
        if line.strip().startswith("2. Be specific for the best results"):
            continue
        if line.strip().startswith("3. Create GEMINI.md files"):
            continue
        # De-duplicate within this chunk
        if line in seen:
            continue
        seen.add(line)
        lines.append(line)
    return "\n".join(lines)


def _gemini_env_path() -> Optional[Path]:
    p = os.environ.get("OMNARA_GEMINI_PATH")
    if not p:
        return None
    p = os.path.expanduser(p)
    path = Path(p)
    if path.is_dir():
        # assume the binary is named 'gemini'
        return path / ("gemini.exe" if os.name == "nt" else "gemini")
    return path


def find_gemini_cli() -> str:
    # 1) Explicit override via env var
    env_path = _gemini_env_path()
    if env_path and env_path.exists() and env_path.is_file():
        return str(env_path)

    # 2) PATH lookup
    if cli := shutil.which("gemini"):
        return cli

    # 3) Common install locations (similar to Claude/Amp patterns)
    locations = [
        Path.home() / ".npm-global/bin/gemini",
        Path("/usr/local/bin/gemini"),
        Path.home() / ".local/bin/gemini",
        Path.home() / "node_modules/.bin/gemini",
        Path.home() / ".yarn/bin/gemini",
    ]
    for path in locations:
        if path.exists() and path.is_file():
            return str(path)

    raise FileNotFoundError(
        "Gemini CLI not found. Ensure 'gemini' v0.6.1 is installed and on PATH, "
        "or set OMNARA_GEMINI_PATH to the binary or its directory."
    )


class MessageProcessor:
    """Minimal message processing: avoids duplicates and sends to Omnara."""

    def __init__(self, wrapper: "GeminiWrapper"):
        self.wrapper = wrapper
        self.web_ui_messages: set[str] = set()
        # Normalized fingerprints of web-sent user messages to suppress echo-mirroring
        self.web_ui_messages_norm: set[str] = set()
        self.last_message_id: Optional[str] = None

    def process_user_message_sync(self, content: str, from_web: bool) -> None:
        if from_web:
            self.web_ui_messages.add(content)
            try:
                fp = re.sub(r"\s+", " ", strip_ansi(content)).strip().lower()
                if fp:
                    self.web_ui_messages_norm.add(fp)
            except Exception:
                pass
            return
        # For unit-testability and simple integrations, allow direct user message send
        try:
            if self.wrapper.agent_instance_id and self.wrapper.omnara_client:
                self.wrapper.omnara_client.send_user_message(
                    agent_instance_id=self.wrapper.agent_instance_id,
                    content=content,
                )
        except Exception:
            pass

    def process_assistant_message_sync(self, content: str) -> None:
        if not content.strip():
            return
        if not self.wrapper.agent_instance_id or not self.wrapper.omnara_client:
            return
        # If a permission prompt is active (we've sent options to the web app),
        # suppress further assistant messages to avoid overwriting the question.
        try:
            ppo = getattr(self.wrapper, "pending_permission_options", {})
            if isinstance(ppo, dict) and ppo:
                return
        except Exception:
            pass
        # Sanitize control chars
        sanitized = "".join(c if ord(c) >= 32 or c in "\n\r\t" else "" for c in content)
        # Remove any leading CWD blob that sometimes prefixes output without newline
        try:
            sanitized = self.wrapper.strip_leading_cwd_blob(sanitized)
        except Exception:
            pass
        did_send = False
        # Heuristically extract echoed user prompt and the assistant rest
        try:
            user_echo, rest = extract_user_echo_and_rest(sanitized)
        except Exception:
            user_echo, rest = None, sanitized
        # Fallback: if first line exactly matches last local input, treat as echoed prompt
        if not user_echo:
            try:
                li = (self.wrapper._last_local_input_line or "").strip()
                if li:
                    if sanitized.startswith(li) and len(sanitized) > len(li):
                        sep = sanitized[len(li) : len(li) + 1]
                        if sep in ("\n", "\r"):
                            user_echo = li
                            rest = sanitized[len(li) + 1 :]
            except Exception:
                pass
        # Mirror the echoed user message once per turn, unless it originated from web UI or was just sent locally
        if user_echo and not self.wrapper._echo_mirrored_turn:
            # Suppress if this echo matches a recent web-sent message
            try:
                fp_user = re.sub(r"\s+", " ", strip_ansi(user_echo)).strip().lower()
                # Suppress if it was injected from web or sent locally moments ago
                if fp_user and (
                    fp_user in getattr(self, "web_ui_messages_norm", set())
                    or fp_user in getattr(self.wrapper, "_recent_local_user_norm", set())
                ):
                    # Consume this fingerprint to avoid repeated suppression
                    try:
                        self.web_ui_messages_norm.discard(fp_user)
                    except Exception:
                        pass
                    try:
                        self.wrapper._recent_local_user_norm.discard(fp_user)
                    except Exception:
                        pass
                    # Mark echo handled for this turn and skip sending
                    try:
                        self.wrapper._echo_mirrored_turn = True
                        self.wrapper._last_local_input_line = None
                    except Exception:
                        pass
                    user_echo = None  # ensure we don't send below
            except Exception:
                pass
        if user_echo and not self.wrapper._echo_mirrored_turn:
            try:
                # Prefer the exact submitted line length to avoid trailing UI noise
                to_send = user_echo
                try:
                    li = (self.wrapper._last_local_input_line or "").strip()
                    if li and user_echo.strip().startswith(li):
                        to_send = li
                except Exception:
                    pass
                # Send the mirrored user message once
                self.wrapper.omnara_client.send_user_message(
                    agent_instance_id=self.wrapper.agent_instance_id, content=to_send
                )
                self.wrapper._echo_mirrored_turn = True
                # Clear last input to avoid any subsequent accidental matches
                self.wrapper._last_local_input_line = None
            except Exception:
                pass
        # Collapse redraw duplicates in the assistant payload (segment-level and sentence-level)
        try:
            rest = collapse_repeated_segments(rest)
            # Ensure dedupe window exists
            if not hasattr(self.wrapper, "_recent_assistant_hashes"):
                self.wrapper._recent_assistant_hashes = set()
            # Sentence-level dedupe within this chunk and against recent sends
            sentences = re.split(r"(?<=[.!?])\s+|[\r\n]+", rest)
            filtered: list[str] = []
            # Reset recent window if long idle (separate turn)
            reset_after = float(os.environ.get("OMNARA_GEMINI_DEDUP_RESET_SECONDS", "10"))
            now = time.time()
            if now - self.wrapper.last_output_time > reset_after:
                self.wrapper._recent_assistant_hashes = set()
            for s in sentences:
                t = s.strip()
                if not t:
                    continue
                fp = re.sub(r"\s+", " ", t).strip().lower()
                if fp in self.wrapper._recent_assistant_hashes:
                    continue
                self.wrapper._recent_assistant_hashes.add(fp)
                filtered.append(t)
            rest = "\n".join(filtered)
        except Exception:
            pass
        try:
            requires_input = bool(getattr(self.wrapper, "_require_user_input_flag", False))
            # Also detect inline phrases and confirmation UI indicating confirmation is needed
            waiting_phrases = (
                "Waiting for your response",
                "Waiting for your input",
                "Waiting for user confirmation",
            )
            # Only mark as requires input from phrases if we didn't already extract a permission prompt
            if not requires_input and not getattr(self.wrapper, "pending_permission_options", {}):
                if any(p.lower() in sanitized.lower() for p in waiting_phrases):
                    requires_input = True
            if not requires_input:
                # If the content includes the apply prompt or options list, treat as a question
                for line in sanitized.splitlines():
                    if CONFIRM_APPLY_RE.match(line) or CONFIRM_OPTION_RE.match(line) or QUERY_HEADER_RE.match(line):
                        requires_input = True
                        break
            # Remove known non-semantic status lines and confirmation UI from content
            cleaned_lines: list[str] = []
            for ln in rest.splitlines():
                raw_ln = ln.strip()
                if not raw_ln:
                    continue
                if CONFIRM_APPLY_RE.match(raw_ln) or CONFIRM_OPTION_RE.match(raw_ln) or QUERY_HEADER_RE.match(raw_ln):
                    continue
                try:
                    if _DEFAULT_NOISE_RE.search(raw_ln) or any(p.search(raw_ln) for p in _EXTRA_NOISE_RES):
                        continue
                except Exception:
                    pass
                cleaned_lines.append(raw_ln)
            rest = "\n".join(cleaned_lines).strip()
            resp = self.wrapper.omnara_client.send_message(
                content=rest,
                agent_type="Gemini",
                agent_instance_id=self.wrapper.agent_instance_id,
                requires_user_input=requires_input,
                git_diff=self.wrapper.git_tracker.get_diff() if self.wrapper.git_tracker else None,
                wait_for_response=False,
            )
            self.last_message_id = getattr(resp, "message_id", None)
            did_send = True
            # Reset flag after sending a question
            if requires_input:
                try:
                    self.wrapper._require_user_input_flag = False
                except Exception:
                    pass
        except Exception:
            pass
        # Fallback path for unit tests/mocks: send a minimal message once
        if not did_send:
            try:
                self.wrapper.omnara_client.send_message(
                    content=sanitized,
                    agent_type="Gemini",
                    agent_instance_id=self.wrapper.agent_instance_id,
                )
            except Exception:
                pass


class GeminiWrapper:
    def __init__(
        self,
        api_key: Optional[str] = None,
        base_url: Optional[str] = None,
        idle_delay: float = 3.5,
        name: Optional[str] = None,
        minimal_term: bool = False,
        agent_instance_id: Optional[str] = None,
    ) -> None:
        self.api_key = api_key or os.environ.get("OMNARA_API_KEY")
        if not self.api_key:
            print("[ERROR] API key is required. Pass --api-key or set OMNARA_API_KEY.")
            raise SystemExit(2)

        self.base_url = (base_url or os.environ.get("OMNARA_API_URL") or "https://agent.omnara.com").rstrip("/")
        # Resolve agent instance id: CLI arg > env OMNARA_AGENT_INSTANCE_ID > env OMNARA_SESSION_ID > new UUID
        provided_instance = agent_instance_id or os.environ.get("OMNARA_AGENT_INSTANCE_ID")
        session_env = os.environ.get("OMNARA_SESSION_ID")
        resolved = provided_instance or session_env or str(uuid.uuid4())
        self.session_uuid = resolved
        # Keep both env vars in sync so other tools can target this instance
        os.environ["OMNARA_SESSION_ID"] = self.session_uuid
        os.environ["OMNARA_AGENT_INSTANCE_ID"] = self.session_uuid
        self.name = name or "Gemini"
        # Minimal terminal mode suppresses terminal feature probes at the source
        self.minimal_term = minimal_term or os.environ.get("OMNARA_GEMINI_MINIMAL_TERM", "0") not in ("0", "false", "False", "")

        self.agent_instance_id: Optional[str] = self.session_uuid
        self.omnara_client = OmnaraClient(
            api_key=self.api_key, base_url=self.base_url, log_func=self.log
        )
        self.git_tracker: Optional[GitDiffTracker] = GitDiffTracker(enabled=True)

        self.message_processor = MessageProcessor(self)

        # PTY/IO state
        self.child_pid: Optional[int] = None
        self.master_fd: Optional[int] = None
        self.running = True
        self.idle_delay = idle_delay
        self.last_output_time = 0.0
        self._assistant_buffer: list[str] = []
        self._user_line_buffer: list[str] = []
        self._web_poll_thread: Optional[threading.Thread] = None
        self._delivered_user_ids: set[str] = set()
        self.write_lock = threading.Lock()
        self._sent_initial_wait: bool = False
        self.heartbeat_thread: Optional[threading.Thread] = None
        self.heartbeat_interval: float = 30.0
        self._wake_sent: bool = False
        self._saw_output: bool = False
        self.original_tty_attrs = None
        self.waiting_for_auth: bool = False
        self.auth_nudge_sent: bool = False
        self.post_auth_nudge_sent: bool = False
        self._esc_buf: str = ""
        self._last_local_input_line: Optional[str] = None
        self._recent_local_user_norm: set[str] = set()
        self._echo_mirrored_turn: bool = False
        self._status_buf: str = ""  # rolling window of plain text for status detection
        self._spawn_time: float = 0.0
        self._tui_ready: bool = False
        self._require_user_input_flag: bool = False
        self._terminal_buffer: str = ""
        self._bracketed_paste_enabled: bool = False
        self.pending_permission_options: dict[str, str] = {}
        self._permission_handled_at: float = 0.0
        self._permission_cooldown_until: float = 0.0
        self._permission_fallback_sent_at: float = 0.0
        # Track last-read to coordinate with server and avoid re-fetch loops
        self._last_read_message_id: Optional[str] = None
        # For pipe (non-PTY) mode
        self._proc: Optional[subprocess.Popen] = None

        # Optional file logging controlled by env var
        self._log_fp = None
        try:
            log_path = os.environ.get("OMNARA_GEMINI_LOG_FILE")
            if log_path:
                # Ensure parent directory exists if a path with folders is provided
                try:
                    Path(log_path).parent.mkdir(parents=True, exist_ok=True)
                except Exception:
                    pass
                self._log_fp = open(log_path, "a", encoding="utf-8", errors="ignore")
                try:
                    atexit.register(lambda: (self._log_fp and (not self._log_fp.closed) and self._log_fp.close()))
                except Exception:
                    pass
                # Note: Use log() after _log_fp set to announce file path
                self.log(f"[gemini] Logging to {log_path}")
        except Exception:
            self._log_fp = None

        # Helpful startup banner
        if os.environ.get("OMNARA_GEMINI_DEBUG"):
            try:
                self.log(
                    f"[gemini] session={self.session_uuid} base_url={self.base_url} name={self.name}"
                )
            except Exception:
                pass

    def log(self, msg: str) -> None:
        try:
            line = (msg or "").rstrip("\n")
            # Only print to stderr when explicit debug is enabled
            if os.environ.get("OMNARA_GEMINI_DEBUG") or os.environ.get("OMNARA_GEMINI_LOG_STDERR"):
                print(line, file=sys.stderr)
            if self._log_fp:
                try:
                    ts = time.strftime("%Y-%m-%d %H:%M:%S")
                    self._log_fp.write(f"{ts} {line}\n")
                    self._log_fp.flush()
                except Exception:
                    pass
        except Exception:
            # Best-effort logging; ignore errors
            pass

    def _flush_assistant_if_idle(self) -> None:
        if not self._assistant_buffer:
            return
        if time.time() - self.last_output_time >= self.idle_delay:
            content = "".join(self._assistant_buffer)
            self._assistant_buffer.clear()
            self.message_processor.process_assistant_message_sync(content)

    def _handle_terminal_queries(self, data: bytes) -> tuple[bytes, list[bytes]]:
        """Detect terminal query sequences from the child and synthesize replies.

        Returns (printable_bytes, responses) where `responses` are bytes to
        write back to the child PTY. Known queries are stripped from printable
        output to avoid artifacts like "^[ [ ? 6 2 c" appearing in the UI.
        """
        text = data.decode(errors="ignore")
        responses: list[bytes] = []

        # Allow disabling via env for troubleshooting (no respond, no strip)
        if os.environ.get("OMNARA_GEMINI_DISABLE_RESPONDER"):
            return data, responses

        # Regex for CSI sequences: ESC [ params letter
        # We only handle a small set used by TUIs for terminal feature probes.
        csi_re = re.compile(r"\x1b\[([0-9;>?]*)([cRn])")

        def reply_for(params: str, final: str) -> Optional[bytes]:
            # Device Attributes primary: ESC[c or ESC[?…c
            if final == "c":
                if params.startswith("?>") or params.startswith(">"):
                    # Secondary DA like ESC[>c or ESC[>0c
                    return b"\x1b[>0;276;0c"  # generic xterm-ish response
                if params.startswith("?"):
                    # DEC private DA variant e.g. ESC[?62c
                    return b"\x1b[?62;1;2c"  # VT220+ features
                # Plain DA ESC[c
                return b"\x1b[?62;1;2c"
            # Device Status Report: ESC[5n
            if final == "n" and params == "5":
                return b"\x1b[0n"  # "OK"
            # Cursor Position Report request: ESC[6n
            if final == "n" and params == "6":
                return b"\x1b[1;1R"  # top-left
            # Cursor Position Report response itself ("R"): not a query; pass through
            return None

        # Work on the accumulated text including any prior partial escape
        combined = self._esc_buf + text
        stripped_parts: list[str] = []
        last = 0
        for m in csi_re.finditer(combined):
            params, final = m.group(1), m.group(2)
            rep = reply_for(params, final)
            if rep is not None:
                responses.append(rep)
                # Exclude this CSI sequence from local terminal output
                stripped_parts.append(combined[last:m.start()])
                last = m.end()

        # Handle potential trailing partial CSI sequence split across reads
        tail = combined[last:]
        # Find the last ESC[ in tail; if present and tail has no full match, buffer it
        buf = ""
        idx = tail.rfind("\x1b[")
        if idx != -1:
            candidate = tail[idx:]
            # If candidate does not match full CSI, buffer it and drop from output
            if not csi_re.fullmatch(candidate):
                buf = candidate
                tail = tail[:idx]

        stripped_parts.append(tail)
        printable = "".join(stripped_parts).encode(errors="ignore")

        # Update buffer (cap to avoid runaway)
        self._esc_buf = buf[:64]

        return printable, responses

    def _append_terminal_buffer(self, plain_text: str) -> None:
        try:
            self._terminal_buffer += plain_text
            # Trim to last 200k chars
            if len(self._terminal_buffer) > 200000:
                self._terminal_buffer = self._terminal_buffer[-200000:]
        except Exception:
            pass

    def _extract_gemini_permission_prompt(self, buffer: str) -> tuple[str | None, list[str]]:
        """Return (question, options) if an apply-confirm prompt is present."""
        try:
            lines = [BRACKET_COLOR_CODE.sub("", ln).strip() for ln in buffer.splitlines()]
            # Find the last occurrence of the apply question
            q_idx = None
            for i in range(len(lines) - 1, -1, -1):
                if CONFIRM_APPLY_RE.match(lines[i]):
                    q_idx = i
                    break
            if q_idx is None:
                return None, []
            question = "Apply this change?"
            # Collect numbered options following q_idx
            opts: list[str] = []
            for ln in lines[q_idx + 1 : q_idx + 10]:  # look ahead up to 10 lines
                m = CONFIRM_OPTION_RE.match(ln)
                if not m:
                    # also allow bullet-prefixed lines with numbers
                    ln2 = BULLET_PREFIX_RE.sub("", ln)
                    m = CONFIRM_OPTION_RE.match(ln2)
                if m:
                    num = m.group(1)
                    label = m.group(2)
                    opts.append(f"{num}. {label}")
            return question, opts
        except Exception:
            return None, []

    def _maybe_send_permission_prompt(self) -> None:
        # Avoid spamming; only once per detection window
        try:
            now = time.time()
            # Do not send while a prompt is already active or during cooldown
            if getattr(self, "pending_permission_options", {}):
                return
            if now < getattr(self, "_permission_cooldown_until", 0.0):
                return
            if (now - self._permission_handled_at) < 1.0:
                return
            q, opts = self._extract_gemini_permission_prompt(self._terminal_buffer)
            if not q:
                waiting_markers = (
                    "Waiting for your response",
                    "Waiting for your input",
                    "Waiting for user confirmation",
                )
                has_waiting = any(m.lower() in self._terminal_buffer.lower() for m in waiting_markers)
                if has_waiting and (now - self._permission_fallback_sent_at) > 0.5:
                    q = "Apply this change?"
                    opts = [
                        "1. Yes, allow once",
                        "2. Yes, allow always",
                        "3. Modify with external editor",
                        "4. No, suggest changes",
                    ]
                    self._permission_fallback_sent_at = now
                else:
                    return
            # Build [OPTIONS] block (web parses this format) and send as question
            if not opts:
                opts = [
                    "1. Yes, allow once",
                    "2. Yes, allow always",
                    "3. Modify with external editor",
                    "4. No, suggest changes",
                ]
            options_text = "\n".join(opts)
            permission_msg = f"{q}\n\n[OPTIONS]\n{options_text}\n[/OPTIONS]"
            if self.omnara_client and self.agent_instance_id:
                self.omnara_client.send_message(
                    content=permission_msg,
                    agent_type="Gemini",
                    agent_instance_id=self.agent_instance_id,
                    requires_user_input=True,
                    wait_for_response=False,
                )
            # Prepare mapping for web responses
            self.pending_permission_options = {
                "1": "1",
                "2": "2",
                "3": "3",
                "4": "4",
                "yes, allow once": "1",
                "yes, allow always": "2",
                "modify with external editor": "3",
                "no, suggest changes": "4",
                "allow once": "1",
                "allow always": "2",
                "edit external": "3",
                "reject": "4",
                "allow_once": "1",
                "allow_always": "2",
                "edit_external": "3",
            }
            self._require_user_input_flag = False
            self._permission_handled_at = now
        except Exception:
            pass

    def strip_leading_cwd_blob(self, content: str) -> str:
        """Strip a leading working-directory token that sometimes prefixes output.

        Examples: "~/proj", "/Users/x/proj", or "sandbox~/proj" directly stuck
        to the beginning of assistant content without whitespace.
        """
        try:
            if not content:
                return content
            cwd = os.getcwd()
            home = str(Path.home())
            cwd_home = cwd.replace(home, "~") if cwd.startswith(home) else cwd
            prefixes = [cwd, cwd_home, f"sandbox{cwd}", f"sandbox{cwd_home}"]
            for p in prefixes:
                if content.startswith(p):
                    return content[len(p) :].lstrip()
        except Exception:
            pass
        return content

    def run_gemini_with_pty(self, extra_args: list[str]) -> int:
        gemini_path = find_gemini_cli()

        child_pid, master_fd = pty.fork()
        if child_pid == 0:
            # Child: exec gemini
            try:
                # Set terminal size if available
                try:
                    cols, rows = os.get_terminal_size()
                    os.environ["COLUMNS"] = str(cols)
                    os.environ["ROWS"] = str(rows)
                except Exception:
                    pass

                # Ensure Omnara env visible to child
                os.environ["OMNARA_API_KEY"] = self.api_key  # noqa: F841
                os.environ["OMNARA_API_URL"] = self.base_url  # noqa: F841
                os.environ["OMNARA_SESSION_ID"] = self.session_uuid  # noqa: F841

                # Suppress terminal capability probing at the source when requested
                if self.minimal_term:
                    os.environ["TERM"] = "dumb"
                    os.environ["NO_COLOR"] = "1"
                    os.environ["CLICOLOR_FORCE"] = "0"
                    os.environ["FORCE_COLOR"] = "0"

                # Exec strategy is configurable: direct | shell | script
                os.environ.setdefault("TERM", "xterm-256color")
                exec_mode = os.environ.get("OMNARA_GEMINI_EXEC_MODE")
                if not exec_mode:
                    exec_mode = "script" if (sys.platform == "darwin" and os.path.exists("/usr/bin/script")) else "direct"
                exec_mode = exec_mode.lower()

                if exec_mode == "shell":
                    shell = (
                        os.environ.get("OMNARA_GEMINI_SHELL")
                        or os.environ.get("SHELL")
                        or "/bin/zsh"
                    )
                    def _q(s: str) -> str:
                        return s.replace("'", "'\\''")
                    cmd = "exec '" + _q(gemini_path) + "'" + (
                        " " + " ".join("'" + _q(a) + "'" for a in extra_args) if extra_args else ""
                    )
                    argv = [shell, "-l", "-i", "-c", cmd]
                elif exec_mode == "script" and sys.platform == "darwin" and os.path.exists("/usr/bin/script"):
                    argv = [
                        "/usr/bin/script",
                        "-q",
                        "/dev/null",
                        gemini_path,
                    ] + extra_args
                else:
                    argv = [gemini_path] + extra_args
                os.execvp(argv[0], argv)
            except Exception as e:
                print(f"[ERROR] Failed to exec gemini: {e}")
                os._exit(1)

        # Parent: PTY configured
        self.child_pid = child_pid
        self.master_fd = master_fd
        # Record spawn time for fallback timers
        try:
            self._spawn_time = time.time()
        except Exception:
            self._spawn_time = 0.0

        # Ensure we restore TTY on any exit
        try:
            atexit.register(self._restore_tty)
        except Exception:
            pass

        # Apply PTY window size to child so curses UIs render
        try:
            cols, rows = shutil.get_terminal_size(fallback=(80, 24))
            self._apply_winsize(rows, cols)
            # Update size on future terminal resizes
            def _resize_handler(_sig, _frm):
                try:
                    c, r = shutil.get_terminal_size(fallback=(80, 24))
                    self._apply_winsize(r, c)
                except Exception:
                    pass

            signal.signal(signal.SIGWINCH, _resize_handler)
        except Exception:
            pass

        # Bootstrap: read initial Gemini output for a short window before any
        # stdin/raw setup or background threads. This mirrors the minimal PTY
        # test behavior and ensures the UI draws immediately.
        try:
            self._bootstrap_read(seconds=2.0)
        except Exception:
            pass

        # Defer starting poller and initial message until auth completes
        self._poll_started = False

        # Kick off heartbeat thread so the agent shows online
        try:
            if not self.heartbeat_thread:
                self.heartbeat_thread = threading.Thread(
                    target=self._heartbeat_loop, daemon=True
                )
                self.heartbeat_thread.start()
        except Exception:
            pass

        # Start the web poller early; injection is still gated by auth/TUI readiness.
        try:
            if not self._poll_started:
                self._start_web_poll_thread()
                self._poll_started = True
        except Exception:
            pass

        # No periodic wake loops; deterministic startup only

        # Put stdin into raw mode so keys (including Enter) are passed directly
        # to the child TUI, but re-enable ISIG so Ctrl-C/Z still generate signals.
        try:
            old_tty = termios.tcgetattr(sys.stdin)
            self.original_tty_attrs = old_tty
            tty.setraw(sys.stdin)
            # Re-enable signal generation (Ctrl-C = SIGINT, Ctrl-Z = SIGTSTP)
            attrs = termios.tcgetattr(sys.stdin)
            attrs[3] = attrs[3] | termios.ISIG  # lflag |= ISIG
            termios.tcsetattr(sys.stdin, termios.TCSADRAIN, attrs)
        except Exception:
            old_tty = None

        # Ensure we forward SIGINT to the child and restore TTY
        def _sigint_handler(signum, frame):
            try:
                self.running = False
                if self.child_pid:
                    os.kill(self.child_pid, signal.SIGINT)
            except Exception:
                pass
        try:
            signal.signal(signal.SIGINT, _sigint_handler)
        except Exception:
            pass

        try:
            self.last_output_time = time.time()

            # Set master fd to non-blocking like Claude
            import fcntl as _fcntl
            flags = _fcntl.fcntl(self.master_fd, _fcntl.F_GETFL)
            _fcntl.fcntl(self.master_fd, _fcntl.F_SETFL, flags | os.O_NONBLOCK)

            while self.running:
                rlist = [self.master_fd, sys.stdin]
                try:
                    readable, _, _ = select.select(rlist, [], [], 0.05)
                except Exception:
                    readable = []

                # No periodic wake nudges here

                for src in readable:
                    if src is sys.stdin:
                        try:
                            data = os.read(sys.stdin.fileno(), 1024)
                        except Exception:
                            data = b""
                        if not data:
                            continue
                        # Echo into gemini
                        try:
                            # Default to CR for Enter to maximize TUI compatibility
                            # Set OMNARA_GEMINI_ENTER_MODE=lf to forward LF instead
                            enter_mode = os.environ.get("OMNARA_GEMINI_ENTER_MODE")
                            if enter_mode != "lf":
                                data_to_send = data.replace(b"\n", b"\r")
                            else:
                                data_to_send = data
                            with self.write_lock:
                                self._write_child(data_to_send)
                        except Exception:
                            pass

                        # Track complete user lines to send to API
                        try:
                            txt = data.decode(errors="ignore")
                            self._user_line_buffer.append(txt)
                            # Treat either LF or CR as a submission delimiter
                            if ("\n" in txt) or ("\r" in txt):
                                line = strip_ansi("".join(self._user_line_buffer))
                                self._user_line_buffer.clear()
                                # Record and immediately send the submitted user line
                                try:
                                    submitted = line.strip()
                                    self._last_local_input_line = submitted
                                    if submitted:
                                        try:
                                            # Normalize and remember to suppress echo mirroring
                                            norm = re.sub(r"\s+", " ", strip_ansi(submitted)).strip().lower()
                                            if norm:
                                                self._recent_local_user_norm.add(norm)
                                            self.omnara_client.send_user_message(
                                                agent_instance_id=self.agent_instance_id,
                                                content=submitted,
                                            )
                                        except Exception:
                                            pass
                                        # Prevent later echo-based mirror from duplicating
                                        self._echo_mirrored_turn = True
                                except Exception:
                                    pass
                                # We mirror once when first assistant output arrives
                        except Exception:
                            pass

                    else:
                        # Output from gemini
                        try:
                            out = os.read(self.master_fd, 4096)
                        except Exception:
                            out = b""
                        if not out:
                            # Child may have exited; flush any pending buffer
                            self._flush_assistant_if_idle()
                            self.running = False
                            break

                        # Track bracketed paste enable/disable sequences in raw bytes
                        try:
                            if b"\x1b[?2004h" in out:
                                self._bracketed_paste_enabled = True
                            if b"\x1b[?2004l" in out:
                                self._bracketed_paste_enabled = False
                        except Exception:
                            pass

                        # Intercept common terminal queries and respond to child
                        printable_bytes, replies = self._handle_terminal_queries(out)
                        for rep in replies:
                            try:
                                with self.write_lock:
                                    self._write_child(rep)
                            except Exception:
                                pass

                        # Write bytes (with terminal queries stripped) to the user terminal
                        text = printable_bytes.decode(errors="ignore")
                        plain = strip_ansi(text)
                        if plain:
                            self._status_buf = (self._status_buf + plain)[-512:]
                            self._append_terminal_buffer(plain)
                            # Try to extract and send a permission prompt to Omnara when present
                            self._maybe_send_permission_prompt()
                            try:
                                # Heuristic that the main TUI has drawn
                                if (
                                    ("Tips for getting started" in self._status_buf)
                                    or ("/help for more information" in self._status_buf)
                                    or ("Ask questions, edit files" in self._status_buf)
                                ):
                                    self._tui_ready = True
                                    if os.environ.get("OMNARA_GEMINI_DEBUG"):
                                        self.log("[gemini] TUI ready detected")
                            except Exception:
                                pass
                        # Prefer explicit auth-complete signals over any lingering "Waiting for auth" text
                        if ("Loaded cached credentials" in self._status_buf) or ("Authenticated" in self._status_buf):
                            self.waiting_for_auth = False
                            self.auth_nudge_sent = False
                            if os.environ.get("OMNARA_GEMINI_DEBUG"):
                                self.log("[gemini] auth complete detected")
                            # Start poller and initial dashboard message once
                            if not self._poll_started:
                                try:
                                    self._start_web_poll_thread()
                                    self._poll_started = True
                                except Exception:
                                    pass
                                try:
                                    if not self._sent_initial_wait:
                                        self._send_initial_waiting_message()
                                except Exception:
                                    pass
                            # Some Gemini builds need a post-auth keypress to enter the TUI
                            if not self.post_auth_nudge_sent and os.environ.get("OMNARA_GEMINI_POST_AUTH_NUDGE", "0") != "0":
                                try:
                                    # Allow tuning via OMNARA_GEMINI_POST_AUTH_DELAYS="0.05,0.25,0.5"
                                    delays_env = os.environ.get("OMNARA_GEMINI_POST_AUTH_DELAYS")
                                    if delays_env:
                                        try:
                                            delays = [float(x.strip()) for x in delays_env.split(",") if x.strip()]
                                        except Exception:
                                            delays = [0.05, 0.25, 0.5]
                                    else:
                                        delays = [0.05, 0.25, 0.5, 1.0]
                                    # Schedule a few CRLF nudges; deterministic, then stop
                                    self._schedule_fixed_handshake(delays, b"\r\n")
                                    if os.environ.get("OMNARA_GEMINI_DEBUG"):
                                        self.log(f"[gemini] scheduled post-auth nudges: {delays}")
                                    self.post_auth_nudge_sent = True
                                except Exception:
                                    pass
                            # Safety fallback: if the TUI still isn’t ready shortly after auth, send extra CRLFs
                            if (os.environ.get("OMNARA_GEMINI_POST_AUTH_NUDGE", "0") != "0") and (not self._tui_ready):
                                try:
                                    self._schedule_fixed_handshake([1.5, 2.0], b"\r\n")
                                    if os.environ.get("OMNARA_GEMINI_DEBUG"):
                                        self.log("[gemini] scheduled safety nudges post-auth")
                                except Exception:
                                    pass
                        elif "Waiting for auth" in self._status_buf:
                            self.waiting_for_auth = True
                            # Single, deterministic auth nudge if enabled
                            if not self.auth_nudge_sent and os.environ.get("OMNARA_GEMINI_AUTO_AUTH_NUDGE", "0") != "0":
                                try:
                                    with self.write_lock:
                                        self._write_child(b"\r\n")
                                    self.auth_nudge_sent = True
                                    if os.environ.get("OMNARA_GEMINI_DEBUG"):
                                        self.log("[gemini] sent auth nudge CRLF")
                                except Exception:
                                    pass
                        else:
                            # Fallback: if auth text is missed (due to split reads / color),
                            # start the poller after a short grace so first-run doesn’t hang.
                            try:
                                fallback_sec = float(os.environ.get("OMNARA_GEMINI_POLL_FALLBACK_SECONDS", "10"))
                            except Exception:
                                fallback_sec = 10.0
                            if (
                                not self._poll_started
                                and not self.waiting_for_auth
                                and self._spawn_time
                                and (time.time() - self._spawn_time) >= fallback_sec
                            ):
                                try:
                                    self._start_web_poll_thread()
                                    self._poll_started = True
                                    # Consider auth complete to allow injection
                                    self.waiting_for_auth = False
                                except Exception:
                                    pass
                                try:
                                    if not self._sent_initial_wait and self.agent_instance_id and self.omnara_client:
                                        # Ensure session is visible in Omnara
                                        self._send_initial_waiting_message()
                                except Exception:
                                    pass
                        # Detect when the CLI is waiting for explicit confirmation
                        try:
                            if (
                                ("Waiting for user confirmation" in self._status_buf)
                                or ("Waiting for your response" in self._status_buf)
                                or ("Waiting for your input" in self._status_buf)
                            ):
                                self._require_user_input_flag = True
                        except Exception:
                            pass

                        sys.stdout.write(text)
                        sys.stdout.flush()

                        # For dashboard only: sanitize and buffer
                        clean = strip_ansi(text)
                        filtered = filter_visual_noise(clean)
                        if filtered:
                            self._assistant_buffer.append(filtered)
                            # Only advance the idle timer when we captured
                            # meaningful (non-noise) output. This prevents
                            # spinner/status noise from starving flushes.
                            self.last_output_time = time.time()
                        self._saw_output = True

                # Periodically flush if idle
                self._flush_assistant_if_idle()

                # Check child process status without blocking
                try:
                    pid, _ = os.waitpid(self.child_pid, os.WNOHANG)
                    if pid == self.child_pid:
                        # Child exited — break loop after final flush
                        self._flush_assistant_if_idle()
                        self.running = False
                except ChildProcessError:
                    self.running = False
                except Exception:
                    pass

        finally:
            # Restore terminal if we changed it
            if old_tty is not None:
                try:
                    termios.tcsetattr(sys.stdin, termios.TCSADRAIN, old_tty)
                except Exception:
                    pass

            # Ensure child is terminated
            try:
                if self.child_pid:
                    os.kill(self.child_pid, signal.SIGTERM)
            except Exception:
                pass

        return 0

    def _restore_tty(self) -> None:
        try:
            if self.original_tty_attrs is not None:
                termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty_attrs)
        except Exception:
            pass

    def _start_web_poll_thread(self) -> None:
        if self._web_poll_thread and self._web_poll_thread.is_alive():
            return

        def _poll_loop() -> None:
            if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                self.log(
                    f"[gemini] poller start: base_url={self.base_url} instance={self.agent_instance_id}"
                )
            # Ensure the Omnara session is visible as soon as the poller starts
            try:
                if not self._sent_initial_wait and self.agent_instance_id and self.omnara_client:
                    self._send_initial_waiting_message()
            except Exception:
                pass
            try:
                poll_interval = float(os.environ.get("OMNARA_GEMINI_POLL_INTERVAL_SECONDS", "0.5"))
            except Exception:
                poll_interval = 0.5
            while self.running:
                try:
                    if not self.agent_instance_id:
                        time.sleep(1.0)
                        continue

                    # Poll without last_read id and deduplicate locally
                    if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                        self.log(
                            f"[gemini] poll: fetching pending (last_read={self._last_read_message_id})"
                        )
                    resp = self.omnara_client.get_pending_messages(
                        self.agent_instance_id, self._last_read_message_id
                    )
                    if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                        try:
                            ids = [m.id for m in resp.messages]
                        except Exception:
                            ids = []
                        self.log(
                            f"[gemini] poll: status={resp.status} count={len(resp.messages)} ids={ids}"
                        )

                    # Another consumer already updated last_read; reset and continue
                    if resp.status == "stale":
                        # Reset local pointer and try again on next tick
                        self._last_read_message_id = None
                        if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                            self.log("[gemini] poll: stale last_read; resetting pointer")
                        time.sleep(min(0.5, poll_interval))
                        continue

                    if resp.messages:
                        # If Gemini is waiting for auth, do not inject messages yet
                        # Defer injection until TUI is ready, but don't block the poller entirely
                        if self.waiting_for_auth and not self._tui_ready:
                            if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                                self.log("[gemini] poll: gating injection due to auth/TUI not ready")
                            time.sleep(min(0.2, poll_interval))
                            continue
                        # Also gate injection until TUI is ready, with a time-based fallback
                        if not self._tui_ready:
                            try:
                                fb = float(os.environ.get("OMNARA_GEMINI_INJECT_FALLBACK_SECONDS", "10"))
                            except Exception:
                                fb = 10.0
                            if self._spawn_time and (time.time() - self._spawn_time) < fb:
                                if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                                    self.log("[gemini] poll: gating injection until TUI ready/fallback")
                                time.sleep(min(0.2, poll_interval))
                                continue
                        # Ensure initial waiting message is emitted once when injection becomes allowed
                        if not self._sent_initial_wait:
                            try:
                                self._send_initial_waiting_message()
                            except Exception:
                                pass
                        if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                            self.log(f"[gemini] poll: {len(resp.messages)} pending user msgs")
                        for msg in resp.messages:
                            if msg.id in self._delivered_user_ids:
                                continue
                            # Mark as from web to avoid duplicate logging
                            self.message_processor.process_user_message_sync(
                                msg.content, from_web=True
                            )
                            try:
                                # Respect ENTER mode for injected messages too
                                enter_mode = os.environ.get("OMNARA_GEMINI_ENTER_MODE")
                                # Default to CRLF for broad TUI compatibility (this may cause a brief flicker)
                                # Options: lf | cr | crlf
                                if enter_mode == "lf":
                                    suffix = "\n"
                                elif enter_mode == "cr":
                                    suffix = "\r"
                                else:
                                    suffix = "\r\n"
                                # If we have pending permission options, map content to a choice number
                                to_send = msg.content.strip()
                                if self.pending_permission_options:
                                    key = to_send.lower().strip()
                                    mapped = self.pending_permission_options.get(key)
                                    if not mapped and key.isdigit() and key in self.pending_permission_options:
                                        mapped = key
                                    # Try to match prefix of any option label
                                    if not mapped:
                                        for k, v in self.pending_permission_options.items():
                                            if not k.isdigit() and k in key:
                                                mapped = v
                                                break
                                    if mapped:
                                        to_send = mapped
                                        # Clear mapping after handling a permission response
                                        self.pending_permission_options = {}
                                        # Prevent immediate re-prompting and clear old buffer
                                        try:
                                            self._permission_cooldown_until = time.time() + 3.0
                                            self._terminal_buffer = ""
                                        except Exception:
                                            pass
                                # Write content + Enter together (prior behavior that ensured submit)
                                payload = (to_send + suffix).encode()
                                with self.write_lock:
                                    self._write_child(payload)
                                # Optional extra Enter (default 'same') — enables reliable submit but can flicker
                                extra_mode_env = os.environ.get("OMNARA_GEMINI_EXTRA_ENTER")
                                extra_mode = (
                                    extra_mode_env.strip().lower() if extra_mode_env else "same"
                                )
                                if msg.content.strip() and extra_mode and extra_mode not in ("0", "none", "false"):
                                    if extra_mode == "same":
                                        extra_suffix = suffix
                                    elif extra_mode == "lf":
                                        extra_suffix = "\n"
                                    elif extra_mode == "cr":
                                        extra_suffix = "\r"
                                    elif extra_mode == "crlf":
                                        extra_suffix = "\r\n"
                                    elif extra_mode == "double":
                                        # Send the configured suffix twice
                                        extra_suffix = suffix + suffix
                                    else:
                                        extra_suffix = suffix  # fallback to same
                                    # Small inter-write gap from prior behavior helped some TUIs draw; harmless flicker acceptable
                                    try:
                                        time.sleep(0.02)
                                    except Exception:
                                        pass
                                    with self.write_lock:
                                        self._write_child(extra_suffix.encode())
                                # No timing-based fallbacks or delays.
                                if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                                    preview = msg.content[:80].replace("\n", "\\n")
                                    # Avoid backslashes inside f-string expression (Python 3.10 quirk)
                                    if suffix == "\r\n":
                                        suffix_name = "CRLF"
                                    elif suffix == "\r":
                                        suffix_name = "CR"
                                    else:
                                        suffix_name = "LF"
                                    log_msg = f"[gemini] inject: msg={msg.id} bytes={len(payload)} suffix={suffix_name}"
                                    if extra_mode and extra_mode not in ("0", "none", "false"):
                                        log_msg += f" extra_enter={extra_mode}"
                                    log_msg += f" preview={preview!r}"
                                    self.log(log_msg)
                            except Exception:
                                pass
                            self._delivered_user_ids.add(msg.id)
                        # Advance server-side last_read to the newest delivered message
                        try:
                            self._last_read_message_id = resp.messages[-1].id
                            if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                                self.log(
                                    f"[gemini] poll: advancing last_read -> {self._last_read_message_id}"
                                )
                        except Exception:
                            pass

                    # Backoff between polls
                    time.sleep(poll_interval)
                except Exception as e:
                    # Log and retry to keep the thread resilient
                    if os.environ.get("OMNARA_GEMINI_LOG_POLL"):
                        self.log(f"[gemini] poll: error {e}")
                    time.sleep(max(1.0, poll_interval))

        t = threading.Thread(target=_poll_loop, name="omnara-gemini-web-poll", daemon=True)
        t.start()
        self._web_poll_thread = t

    def _send_initial_waiting_message(self) -> None:
        if self._sent_initial_wait:
            return
        if not self.agent_instance_id or not self.omnara_client:
            return
        # Short, neutral message that clearly reflects idle state
        content = "Gemini session started. Waiting for your input."
        try:
            self.omnara_client.send_message(
                content=content,
                agent_type="Gemini",
                agent_instance_id=self.agent_instance_id,
                requires_user_input=False,
                wait_for_response=False,
            )
            self._sent_initial_wait = True
        except Exception:
            # Ignore errors; not critical for session operation
            pass

    def _heartbeat_loop(self) -> None:
        if not self.omnara_client:
            return
        session = self.omnara_client.session
        url = (
            self.base_url.rstrip("/")
            + f"/api/v1/agents/instances/{self.agent_instance_id}/heartbeat"
        )
        # Initial immediate heartbeat to mark online quickly
        try:
            session.post(url, timeout=10)
        except Exception:
            pass
        # Periodic heartbeats with jitter
        import random

        while self.running:
            try:
                resp = session.post(url, timeout=10)
                if getattr(resp, "status_code", 200) >= 400:
                    # Non-fatal
                    pass
            except Exception:
                pass
            delay = self.heartbeat_interval + random.uniform(-2.0, 2.0)
            if delay < 5:
                delay = 5
            for _ in range(int(delay * 10)):
                if not self.running:
                    break
                time.sleep(0.1)

    def _bootstrap_read(self, seconds: float = 2.0) -> None:
        """Read any initial output from the child PTY and echo to terminal.

        This mirrors the minimal PTY test that showed Gemini outputs without
        input. We run this before setting stdin raw or starting background
        threads so the initial TUI draw is visible immediately.
        """
        end = time.time() + max(0.0, seconds)
        while time.time() < end and self.master_fd is not None:
            try:
                r, _, _ = select.select([self.master_fd], [], [], 0.1)
            except Exception:
                r = []
            if not r:
                continue
            try:
                out = os.read(self.master_fd, 4096)
            except Exception:
                out = b""
            if not out:
                break

            # Intercept queries even during bootstrap and respond immediately
            printable_bytes, replies = self._handle_terminal_queries(out)
            for rep in replies:
                try:
                    with self.write_lock:
                        self._write_child(rep)
                except Exception:
                    pass

            text = printable_bytes.decode(errors="ignore")
            sys.stdout.write(text)
            sys.stdout.flush()
            # Also feed sanitized content to buffer for dashboard logging
            clean = strip_ansi(text)
            filtered = filter_visual_noise(clean)
            if filtered:
                self._assistant_buffer.append(filtered)

    def _schedule_fixed_handshake(self, delays: list[float], payload: bytes) -> None:
        """Send a fixed number of nudges at specific delays, then stop.

        This is deterministic: no conditionals, no indefinite loops. It simply
        sleeps for each delay and writes the payload once.
        """
        def _run():
            for d in delays:
                try:
                    time.sleep(d)
                    if not self.running or (self.master_fd is None and self._proc is None):
                        return
                    with self.write_lock:
                        self._write_child(payload)
                except Exception:
                    return

        t = threading.Thread(target=_run, name="omnara-gemini-fixed-handshake", daemon=True)
        t.start()

    # Removed complex handshake; use single CRLF only for deterministic startup

    def _apply_winsize(self, rows: int, cols: int) -> None:
        """Apply window size to the PTY master so child sees correct size."""
        if self.master_fd is None:
            return
        TIOCSWINSZ = 0x5414
        if sys.platform == "darwin":
            TIOCSWINSZ = 0x80087467
        winsize = struct.pack("HHHH", rows, cols, 0, 0)
        try:
            fcntl.ioctl(self.master_fd, TIOCSWINSZ, winsize)
        except Exception:
            pass

        # Deterministic startup only: no background wake loop

    def _write_child(self, data: bytes) -> None:
        """Write bytes to the child, supporting both PTY and pipe modes."""
        if self.master_fd is not None:
            os.write(self.master_fd, data)
        elif self._proc and self._proc.stdin:
            try:
                self._proc.stdin.write(data)
                self._proc.stdin.flush()
            except Exception:
                pass

    def run_gemini_with_pipes(self, extra_args: list[str]) -> int:
        gemini_path = find_gemini_cli()

        # Prepare environment
        env = os.environ.copy()
        env["OMNARA_API_KEY"] = self.api_key
        env["OMNARA_API_URL"] = self.base_url
        env["OMNARA_SESSION_ID"] = self.session_uuid
        # Force no TTY behavior
        if self.minimal_term:
            env["TERM"] = "dumb"
            env["NO_COLOR"] = "1"
            env["CLICOLOR_FORCE"] = "0"
            env["FORCE_COLOR"] = "0"

        argv = [gemini_path] + extra_args
        try:
            self._proc = subprocess.Popen(
                argv,
                stdin=subprocess.PIPE,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                env=env,
                bufsize=0,
            )
        except Exception as e:
            print(f"[ERROR] Failed to exec gemini: {e}")
            return 1

        # Record spawn time for fallback timers
        try:
            self._spawn_time = time.time()
        except Exception:
            self._spawn_time = 0.0

        # Start heartbeat and poll threads immediately in pipe mode
        try:
            if not self.heartbeat_thread:
                self.heartbeat_thread = threading.Thread(
                    target=self._heartbeat_loop, daemon=True
                )
                self.heartbeat_thread.start()
        except Exception:
            pass

        if not self._poll_started:
            try:
                self._start_web_poll_thread()
                self._poll_started = True
            except Exception:
                pass
            try:
                if not self._sent_initial_wait:
                    self._send_initial_waiting_message()
            except Exception:
                pass

        # Non-blocking fds
        try:
            for f in (self._proc.stdout, self._proc.stderr):
                if f:
                    fl = fcntl.fcntl(f.fileno(), fcntl.F_GETFL)
                    fcntl.fcntl(f.fileno(), fcntl.F_SETFL, fl | os.O_NONBLOCK)
        except Exception:
            pass

        self.last_output_time = time.time()

        # Event loop using select on pipes + stdin (cooked mode)
        try:
            while self.running:
                fds = []
                if self._proc.stdout:
                    fds.append(self._proc.stdout.fileno())
                if self._proc.stderr:
                    fds.append(self._proc.stderr.fileno())
                try:
                    fds.append(sys.stdin.fileno())
                except Exception:
                    pass
                try:
                    rlist, _, _ = select.select(fds, [], [], 0.05)
                except Exception:
                    rlist = []

                for fd in rlist:
                    if self._proc.stdout and fd == self._proc.stdout.fileno():
                        try:
                            out = self._proc.stdout.read() or b""
                        except Exception:
                            out = b""
                        if out:
                            text = out.decode(errors="ignore")
                            sys.stdout.write(text)
                            sys.stdout.flush()
                            clean = strip_ansi(text)
                            filtered = filter_visual_noise(clean)
                            if filtered:
                                self._assistant_buffer.append(filtered)
                                # Only bump idle timer for meaningful output
                                self.last_output_time = time.time()
                    elif self._proc.stderr and fd == self._proc.stderr.fileno():
                        try:
                            err = self._proc.stderr.read() or b""
                        except Exception:
                            err = b""
                        if err:
                            text = err.decode(errors="ignore")
                            sys.stderr.write(text)
                            sys.stderr.flush()
                    else:
                        # stdin from user
                        try:
                            data = os.read(sys.stdin.fileno(), 1024)
                        except Exception:
                            data = b""
                        if not data:
                            continue
                        # In pipe mode default Enter to LF; set ENTER_MODE=cr to use CR
                        enter_mode = os.environ.get("OMNARA_GEMINI_ENTER_MODE")
                        if enter_mode == "cr":
                            data_to_send = data.replace(b"\n", b"\r")
                        else:
                            data_to_send = data
                        with self.write_lock:
                            self._write_child(data_to_send)

                        # Track lines for echo mirroring heuristic
                        try:
                            txt = data.decode(errors="ignore")
                            self._user_line_buffer.append(txt)
                            if ("\n" in txt) or ("\r" in txt):
                                line = strip_ansi("".join(self._user_line_buffer))
                                self._user_line_buffer.clear()
                                try:
                                    submitted = line.strip()
                                    self._last_local_input_line = submitted
                                    if submitted:
                                        try:
                                            norm = re.sub(r"\s+", " ", strip_ansi(submitted)).strip().lower()
                                            if norm:
                                                self._recent_local_user_norm.add(norm)
                                            self.omnara_client.send_user_message(
                                                agent_instance_id=self.agent_instance_id,
                                                content=submitted,
                                            )
                                        except Exception:
                                            pass
                                        self._echo_mirrored_turn = True
                                except Exception:
                                    pass
                        except Exception:
                            pass

                # Flush buffered assistant text if idle
                self._flush_assistant_if_idle()

                # Check process exit
                if self._proc and self._proc.poll() is not None:
                    self._flush_assistant_if_idle()
                    self.running = False

        finally:
            try:
                if self._proc and self._proc.poll() is None:
                    self._proc.terminate()
            except Exception:
                pass

        return 0


def main() -> None:
    parser = argparse.ArgumentParser(description="Gemini CLI wrapper for Omnara")
    parser.add_argument("--api-key", type=str, default=None)
    parser.add_argument("--base-url", type=str, default=None)
    parser.add_argument("--name", type=str, default=None)
    parser.add_argument("--idle-delay", type=float, default=3.5)
    parser.add_argument("--agent-instance-id", type=str, default=None, help="Use an existing agent instance ID (overrides OMNARA_AGENT_INSTANCE_ID)")
    parser.add_argument("--minimal-term", action="store_true", help="Run child with TERM=dumb to prevent terminal probes and color codes")

    # Collect unknown args to forward to the gemini CLI
    args, unknown = parser.parse_known_args()

    wrapper = GeminiWrapper(
        api_key=args.api_key,
        base_url=args.base_url,
        idle_delay=args.idle_delay,
        name=args.name,
        minimal_term=args.minimal_term,
        agent_instance_id=args.agent_instance_id,
    )

    try:
        # Deterministic non-TTY mode when requested
        mode = os.environ.get("OMNARA_GEMINI_MODE", "pty").lower()
        if mode == "pipe":
            wrapper.run_gemini_with_pipes(extra_args=unknown)
        else:
            wrapper.run_gemini_with_pty(extra_args=unknown)
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
