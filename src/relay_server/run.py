"""Legacy entry point - redirects to new FastAPI-based relay server."""

from __future__ import annotations

from relay_server.app import main

if __name__ == "__main__":
    main()
