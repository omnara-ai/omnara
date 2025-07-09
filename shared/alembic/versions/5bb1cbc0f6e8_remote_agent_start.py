"""remote_agent_start

Restructure agent system to support remote agent starting via webhooks.
Major changes:
- Create user_agents table to store user-specific agent configurations
- Drop agent_types table (replaced by user_agents)
- Update agent_instances to reference user_agents instead of agent_types
- All agents can now optionally have webhooks for remote triggering

IMPORTANT MIGRATION NOTES:
1. This migration should be run with the application stopped to avoid concurrency issues
2. Ensure you have a database backup before running
3. The downgrade will lose webhook configuration data (webhook_url and webhook_api_key)
4. All agent_instances must have valid agent_type_id references before migration

Revision ID: 5bb1cbc0f6e8
Revises: e80f941f1bf8
Create Date: 2025-07-04 19:46:15.011478

"""

from typing import Sequence, Union
from uuid import uuid4
import logging

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "5bb1cbc0f6e8"
down_revision: Union[str, None] = "e80f941f1bf8"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None

logger = logging.getLogger(__name__)


def upgrade() -> None:
    """
    Migrate from shared agent_types to user-specific user_agents.
    This is a high-stakes migration that preserves all existing data relationships.
    """

    # Create user_agents table with all required columns and defaults
    op.create_table(
        "user_agents",
        sa.Column("id", sa.UUID(), nullable=False),
        sa.Column("user_id", sa.UUID(), nullable=False),
        sa.Column("name", sa.String(length=255), nullable=False),
        sa.Column("webhook_url", sa.Text(), nullable=True),
        sa.Column("webhook_api_key", sa.Text(), nullable=True),
        sa.Column("is_active", sa.Boolean(), nullable=False, server_default="true"),
        sa.Column(
            "created_at",
            sa.DateTime(),
            nullable=False,
            server_default=sa.text("CURRENT_TIMESTAMP"),
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(),
            nullable=False,
            server_default=sa.text("CURRENT_TIMESTAMP"),
        ),
        sa.ForeignKeyConstraint(
            ["user_id"],
            ["users.id"],
        ),
        sa.PrimaryKeyConstraint("id"),
    )
    # Create index on user_id for faster queries filtering by user
    op.create_index("ix_user_agents_user_id", "user_agents", ["user_id"], unique=False)

    # Add user_agent_id column to agent_instances as nullable initially
    op.add_column(
        "agent_instances", sa.Column("user_agent_id", sa.UUID(), nullable=True)
    )

    # Data migration: Create user_agents for each unique user/agent_type combination
    connection = op.get_bind()

    # First, check for orphaned agent_instances with invalid agent_type_id
    orphan_check = connection.execute(
        sa.text("""
            SELECT COUNT(*) FROM agent_instances
            WHERE agent_type_id IS NULL
            OR agent_type_id NOT IN (SELECT id FROM agent_types)
        """)
    )
    orphan_count = orphan_check.scalar() or 0

    if orphan_count > 0:
        raise RuntimeError(
            f"Cannot proceed: Found {orphan_count} agent_instances with invalid agent_type_id. "
            "These must be cleaned up before migration."
        )

    # Check for agent_instances with invalid user_id references
    invalid_user_check = connection.execute(
        sa.text("""
            SELECT COUNT(*) FROM agent_instances
            WHERE user_id NOT IN (SELECT id FROM users)
        """)
    )
    invalid_user_count = invalid_user_check.scalar() or 0

    if invalid_user_count > 0:
        raise RuntimeError(
            f"Cannot proceed: Found {invalid_user_count} agent_instances with invalid user_id. "
            "These must be cleaned up before migration."
        )

    # Check if there are any agent_instances to migrate
    count_result = connection.execute(sa.text("SELECT COUNT(*) FROM agent_instances"))
    instance_count = count_result.scalar() or 0

    if instance_count > 0:
        logger.info(f"Migrating {instance_count} agent instances...")

        # Get all unique user_id, agent_type_id combinations with agent names
        # Use DISTINCT ON to handle potential duplicates at the database level
        result = connection.execute(
            sa.text("""
                SELECT DISTINCT ON (ai.user_id, at.name)
                    ai.user_id, ai.agent_type_id, at.name
                FROM agent_instances ai
                JOIN agent_types at ON ai.agent_type_id = at.id
                ORDER BY ai.user_id, at.name, ai.started_at DESC
            """)
        )

        # Create user_agents for each combination and build mapping
        user_agent_mapping = {}
        rows_processed = 0

        for row in result:
            user_id, agent_type_id, agent_name = row
            new_id = str(uuid4())

            # Keep the original case of the agent name to maintain compatibility
            # Check if this user_agent already exists (for idempotency)
            existing_check = connection.execute(
                sa.text("""
                    SELECT id FROM user_agents
                    WHERE user_id = :user_id AND name = :name
                """),
                {"user_id": str(user_id), "name": agent_name},
            )
            existing_row = existing_check.fetchone()

            if existing_row:
                # Use existing user_agent
                user_agent_mapping[(str(user_id), str(agent_type_id))] = str(
                    existing_row[0]
                )
                logger.info(
                    f"Using existing user_agent for user {user_id}, agent {agent_name}"
                )
            else:
                # Create new user_agent
                try:
                    connection.execute(
                        sa.text("""
                            INSERT INTO user_agents (id, user_id, name, is_active, created_at, updated_at)
                            VALUES (:id, :user_id, :name, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
                        """),
                        {"id": new_id, "user_id": str(user_id), "name": agent_name},
                    )

                    # Store mapping for both the specific agent_type_id and the user/name combination
                    user_agent_mapping[(str(user_id), str(agent_type_id))] = new_id
                    rows_processed += 1

                except Exception as e:
                    logger.error(
                        f"Failed to insert user_agent for user {user_id}, agent {agent_name}: {e}"
                    )
                    raise

        logger.info(f"Created {rows_processed} user_agent entries")

        # Update agent_instances with new user_agent_id
        # Use a single UPDATE with a JOIN for better performance
        update_result = connection.execute(
            sa.text("""
                UPDATE agent_instances ai
                SET user_agent_id = ua.id
                FROM user_agents ua, agent_types at
                WHERE ai.user_id = ua.user_id
                AND ai.agent_type_id = at.id
                AND ua.name = at.name
            """)
        )

        logger.info(f"Updated {update_result.rowcount} agent_instance records")

        # Verify all agent_instances have been updated
        orphan_check = connection.execute(
            sa.text("SELECT COUNT(*) FROM agent_instances WHERE user_agent_id IS NULL")
        )
        orphan_count = orphan_check.scalar() or 0

        if orphan_count > 0:
            raise RuntimeError(
                f"Migration failed: {orphan_count} agent_instances have no user_agent_id. "
                "This indicates a data integrity issue that must be resolved manually."
            )
    else:
        logger.info("No agent instances to migrate")

    # Add unique constraint AFTER data migration to avoid conflicts
    op.create_unique_constraint(
        "uq_user_agents_user_id_name", "user_agents", ["user_id", "name"]
    )

    # Create foreign key for user_agent_id
    op.create_foreign_key(
        "agent_instances_user_agent_id_fkey",
        "agent_instances",
        "user_agents",
        ["user_agent_id"],
        ["id"],
    )

    # Make user_agent_id NOT NULL now that all data is migrated
    op.alter_column("agent_instances", "user_agent_id", nullable=False)

    # Drop the old foreign key constraint
    op.drop_constraint(
        "agent_instances_agent_type_id_fkey", "agent_instances", type_="foreignkey"
    )

    # Drop agent_type_id column
    op.drop_column("agent_instances", "agent_type_id")

    # Finally, drop agent_types table
    op.drop_table("agent_types")

    logger.info("Migration completed successfully")


def downgrade() -> None:
    """
    Reverse the migration, restoring the shared agent_types structure.
    This preserves all data by recreating agent_types from unique user_agent names.
    """

    # Recreate agent_types table
    op.create_table(
        "agent_types",
        sa.Column("id", sa.UUID(), autoincrement=False, nullable=False),
        sa.Column("name", sa.VARCHAR(length=255), autoincrement=False, nullable=False),
        sa.Column(
            "created_at",
            postgresql.TIMESTAMP(),
            autoincrement=False,
            nullable=False,
            server_default=sa.text("CURRENT_TIMESTAMP"),
        ),
        sa.PrimaryKeyConstraint("id", name="agent_types_pkey"),
        sa.UniqueConstraint(
            "name",
            name="agent_types_name_key",
        ),
    )
    op.create_index("ix_agent_types_id", "agent_types", ["id"], unique=False)

    # Add agent_type_id column back to agent_instances
    op.add_column(
        "agent_instances",
        sa.Column("agent_type_id", sa.UUID(), autoincrement=False, nullable=True),
    )

    # Migrate data back
    connection = op.get_bind()

    # Check if there are any user_agents to migrate back
    count_result = connection.execute(sa.text("SELECT COUNT(*) FROM user_agents"))
    agent_count = count_result.scalar() or 0

    if agent_count > 0:
        logger.info(f"Migrating {agent_count} user agents back to agent types...")

        # Get all unique agent names from user_agents
        result = connection.execute(
            sa.text("SELECT DISTINCT name FROM user_agents ORDER BY name")
        )

        # Create agent_types for each unique name
        for row in result:
            agent_name = row[0]
            new_id = str(uuid4())

            try:
                connection.execute(
                    sa.text("""
                        INSERT INTO agent_types (id, name, created_at)
                        VALUES (:id, :name, CURRENT_TIMESTAMP)
                    """),
                    {"id": new_id, "name": agent_name},
                )
            except Exception as e:
                logger.error(f"Failed to insert agent_type {agent_name}: {e}")
                raise

        # Update agent_instances with agent_type_id based on user_agent name
        update_result = connection.execute(
            sa.text("""
                UPDATE agent_instances ai
                SET agent_type_id = at.id
                FROM user_agents ua, agent_types at
                WHERE ai.user_agent_id = ua.id
                AND ua.name = at.name
            """)
        )

        logger.info(f"Updated {update_result.rowcount} agent_instance records")
    else:
        logger.info("No user agents to migrate back")

    # Make agent_type_id NOT NULL now that data is migrated
    op.alter_column("agent_instances", "agent_type_id", nullable=False)

    # Create foreign key for agent_type_id
    op.create_foreign_key(
        "agent_instances_agent_type_id_fkey",
        "agent_instances",
        "agent_types",
        ["agent_type_id"],
        ["id"],
    )

    # Drop the foreign key constraint for user_agent_id
    op.drop_constraint(
        "agent_instances_user_agent_id_fkey", "agent_instances", type_="foreignkey"
    )

    # Drop user_agent_id column
    op.drop_column("agent_instances", "user_agent_id")

    # Drop indexes and constraints
    op.drop_constraint("uq_user_agents_user_id_name", "user_agents", type_="unique")
    op.drop_index("ix_user_agents_user_id", table_name="user_agents")

    # Drop user_agents table
    op.drop_table("user_agents")

    logger.info("Downgrade completed successfully")
