#!/usr/bin/env python3
"""
Pre-commit hook to check if database schema changes require a new migration.

This script detects changes to SQLAlchemy models and ensures that a corresponding
Alembic migration has been created in the same commit.
"""

import subprocess
import sys
from pathlib import Path


def get_staged_files():
    """Get list of staged files in the current commit."""
    try:
        result = subprocess.run(
            ["git", "diff", "--cached", "--name-only"],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip().split("\n") if result.stdout.strip() else []
    except subprocess.CalledProcessError:
        return []


def has_schema_changes(staged_files):
    """Check if any staged files contain database schema changes."""
    schema_files = ["shared/database/models.py", "shared/database/enums.py"]

    return any(file_path in staged_files for file_path in schema_files)


def has_new_migration(staged_files):
    """Check if any new migration files are being added."""
    migration_files = [
        f
        for f in staged_files
        if f.startswith("shared/alembic/versions/") and f.endswith(".py")
    ]
    return len(migration_files) > 0


def get_migration_files():
    """Get list of existing migration files."""
    migrations_dir = Path("shared/alembic/versions")
    if not migrations_dir.exists():
        return []

    return [f.name for f in migrations_dir.glob("*.py") if f.is_file()]


def main():
    """Main function to check if migration is needed."""
    staged_files = get_staged_files()

    if not staged_files:
        # No staged files, nothing to check
        sys.exit(0)

    schema_changed = has_schema_changes(staged_files)
    new_migration = has_new_migration(staged_files)

    if schema_changed and not new_migration:
        print("❌ ERROR: Database schema changes detected without a migration!")
        print()
        print("You have modified database schema files:")
        for file_path in staged_files:
            if file_path in ["shared/database/models.py", "shared/database/enums.py"]:
                print(f"  - {file_path}")
        print()
        print("You must create an Alembic migration before committing:")
        print("  1. cd shared/")
        print("  2. alembic revision --autogenerate -m 'Describe your changes'")
        print("  3. Review the generated migration file")
        print("  4. git add the new migration file")
        print("  5. git commit again")
        print()
        print("This ensures database changes are properly versioned and deployable.")
        sys.exit(1)

    if schema_changed and new_migration:
        print("✅ Schema changes detected with corresponding migration - good!")

    # Success case
    sys.exit(0)


if __name__ == "__main__":
    main()
