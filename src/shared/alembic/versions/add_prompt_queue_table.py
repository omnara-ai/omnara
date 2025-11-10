"""Add prompt queue table

Revision ID: queue_prompts_001
Revises: 8f18d049395f
Create Date: 2025-11-10 00:00:00.000000

"""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "queue_prompts_001"
down_revision: Union[str, None] = "8f18d049395f"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Create prompt_queue table
    op.create_table(
        "prompt_queue",
        sa.Column("id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column("user_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column(
            "agent_instance_id", postgresql.UUID(as_uuid=True), nullable=False
        ),
        sa.Column("prompt_text", sa.Text(), nullable=False),
        sa.Column("position", sa.Integer(), nullable=False),
        sa.Column(
            "status",
            sa.String(length=20),
            nullable=False,
            server_default="PENDING",
        ),
        sa.Column("created_at", sa.DateTime(), nullable=False),
        sa.Column("sent_at", sa.DateTime(), nullable=True),
        sa.Column("error_message", sa.Text(), nullable=True),
        sa.Column("retry_count", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("message_id", postgresql.UUID(as_uuid=True), nullable=True),
        sa.ForeignKeyConstraint(
            ["agent_instance_id"],
            ["agent_instances.id"],
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["message_id"],
            ["messages.id"],
            ondelete="SET NULL",
        ),
        sa.ForeignKeyConstraint(
            ["user_id"],
            ["users.id"],
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id"),
    )

    # Create indexes
    op.create_index(
        "idx_prompt_queue_instance",
        "prompt_queue",
        ["agent_instance_id"],
        unique=False,
    )
    op.create_index(
        "idx_prompt_queue_status", "prompt_queue", ["status"], unique=False
    )
    op.create_index(
        "idx_prompt_queue_user", "prompt_queue", ["user_id"], unique=False
    )
    op.create_index(
        "uq_prompt_queue_instance_position",
        "prompt_queue",
        ["agent_instance_id", "position"],
        unique=True,
    )


def downgrade() -> None:
    # Drop indexes
    op.drop_index("uq_prompt_queue_instance_position", table_name="prompt_queue")
    op.drop_index("idx_prompt_queue_user", table_name="prompt_queue")
    op.drop_index("idx_prompt_queue_status", table_name="prompt_queue")
    op.drop_index("idx_prompt_queue_instance", table_name="prompt_queue")

    # Drop table
    op.drop_table("prompt_queue")
