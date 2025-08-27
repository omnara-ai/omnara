"""Add webhook_type and webhook_config to user_agents

Revision ID: 2d23b5a05254
Revises: 9f61865b8ba8
Create Date: 2025-08-26 15:04:31.201303

"""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa
from sqlalchemy.dialects import postgresql

# revision identifiers, used by Alembic.
revision: str = "2d23b5a05254"
down_revision: Union[str, None] = "9f61865b8ba8"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Add new columns
    op.add_column(
        "user_agents",
        sa.Column(
            "webhook_type",
            sa.String(length=50),
            nullable=False,
            server_default="DEFAULT",
        ),
    )
    op.add_column(
        "user_agents",
        sa.Column(
            "webhook_config",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=False,
            server_default="{}",
        ),
    )

    # Migrate existing webhook data to new format
    op.execute("""
        UPDATE user_agents
        SET webhook_config = jsonb_build_object(
            'url', webhook_url,
            'api_key', webhook_api_key
        )
        WHERE webhook_url IS NOT NULL
    """)

    # Update webhook_config to remove null api_key if not present
    op.execute("""
        UPDATE user_agents
        SET webhook_config = webhook_config - 'api_key'
        WHERE webhook_config->>'api_key' IS NULL
    """)

    # Drop the old columns
    op.drop_column("user_agents", "webhook_url")
    op.drop_column("user_agents", "webhook_api_key")


def downgrade() -> None:
    # Re-add old columns
    op.add_column("user_agents", sa.Column("webhook_api_key", sa.Text(), nullable=True))
    op.add_column("user_agents", sa.Column("webhook_url", sa.Text(), nullable=True))

    # Migrate data back from webhook_config
    op.execute("""
        UPDATE user_agents
        SET webhook_url = webhook_config->>'url',
            webhook_api_key = webhook_config->>'api_key'
        WHERE webhook_config IS NOT NULL AND webhook_config != '{}'
    """)

    # Drop new columns
    op.drop_column("user_agents", "webhook_config")
    op.drop_column("user_agents", "webhook_type")
