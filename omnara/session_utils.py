"""Session utilities for managing multiple Omnara webhook sessions"""

import json
import os
import socket
from pathlib import Path
from typing import Dict, List, Optional
from datetime import datetime

# Session file location
SESSION_DIR = Path.home() / ".omnara"
SESSION_FILE = SESSION_DIR / "sessions.json"


def is_port_available(port: int) -> bool:
    """Check if a port is available for binding"""
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.bind(("", port))
            return True
    except OSError:
        return False


def find_available_port(start_port: int = 6662, max_attempts: int = 10) -> Optional[int]:
    """Find an available port starting from start_port"""
    for i in range(max_attempts):
        port = start_port + i
        if is_port_available(port):
            return port
    return None


def load_sessions() -> Dict:
    """Load session data from file"""
    if not SESSION_FILE.exists():
        return {"sessions": []}
    
    try:
        with open(SESSION_FILE, "r") as f:
            return json.load(f)
    except Exception:
        return {"sessions": []}


def save_sessions(data: Dict):
    """Save session data to file"""
    SESSION_DIR.mkdir(parents=True, exist_ok=True)
    
    with open(SESSION_FILE, "w") as f:
        json.dump(data, f, indent=2)
    
    # Set file permissions to 600 (owner read/write only)
    os.chmod(SESSION_FILE, 0o600)


def add_session(session_name: str, port: int, working_dir: str, pid: Optional[int] = None):
    """Add a new session to tracking"""
    data = load_sessions()
    
    # Check if session already exists
    for session in data["sessions"]:
        if session["name"] == session_name:
            # Update existing session
            session["port"] = port
            session["working_dir"] = working_dir
            session["pid"] = pid
            session["updated_at"] = datetime.now().isoformat()
            save_sessions(data)
            return
    
    # Add new session
    data["sessions"].append({
        "name": session_name,
        "port": port,
        "working_dir": working_dir,
        "pid": pid,
        "started_at": datetime.now().isoformat(),
        "updated_at": datetime.now().isoformat()
    })
    
    save_sessions(data)


def remove_session(session_name: str):
    """Remove a session from tracking"""
    data = load_sessions()
    data["sessions"] = [s for s in data["sessions"] if s["name"] != session_name]
    save_sessions(data)


def get_session_by_name(session_name: str) -> Optional[Dict]:
    """Get session info by name"""
    data = load_sessions()
    for session in data["sessions"]:
        if session["name"] == session_name:
            return session
    return None


def get_active_sessions() -> List[Dict]:
    """Get all active sessions"""
    data = load_sessions()
    return data.get("sessions", [])


def get_session_by_port(port: int) -> Optional[Dict]:
    """Get session info by port"""
    data = load_sessions()
    for session in data["sessions"]:
        if session.get("port") == port:
            return session
    return None


def cleanup_stale_sessions():
    """Remove sessions where the port is no longer in use"""
    data = load_sessions()
    active_sessions = []
    
    for session in data["sessions"]:
        port = session.get("port")
        if port and not is_port_available(port):
            # Port is in use, session might still be active
            active_sessions.append(session)
        else:
            # Port is available, session is likely dead
            print(f"[INFO] Removing stale session: {session['name']}")
    
    data["sessions"] = active_sessions
    save_sessions(data)