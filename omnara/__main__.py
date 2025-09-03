"""Module entry point for `python -m omnara`.

Delegates to `omnara.cli.main` so both the console script (`omnara`) and the
module invocation form work consistently.
"""

from .cli import main as _main


def main() -> None:  # pragma: no cover - thin shim
    _main()


if __name__ == "__main__":  # pragma: no cover
    main()
