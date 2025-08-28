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
    # Add new columns as nullable first
    op.add_column(
        "user_agents",
        sa.Column(
            "webhook_type",
            sa.String(length=50),
            nullable=True,
        ),
    )
    op.add_column(
        "user_agents",
        sa.Column(
            "webhook_config",
            postgresql.JSONB(astext_type=sa.Text()),
            nullable=True,
        ),
    )

    # Migrate existing webhook data to new format
    # For rows with webhook_url, set type to OMNARA_SERVE and migrate config
    op.execute("""
        UPDATE user_agents
        SET webhook_type = 'OMNARA_SERVE',
            webhook_config = jsonb_build_object(
                'url', webhook_url,
                'api_key', COALESCE(webhook_api_key, '')
            )
        WHERE webhook_url IS NOT NULL
    """)

    # For rows without webhook_url, keep webhook_type and webhook_config as NULL
    # (they're already NULL from the column addition)

    # Remove empty api_key from webhook_config if it's an empty string
    op.execute("""
        UPDATE user_agents
        SET webhook_config = webhook_config - 'api_key'
        WHERE webhook_config IS NOT NULL
        AND (webhook_config->>'api_key' = '' OR webhook_config->>'api_key' IS NULL)
    """)

    # Drop the old columns
    op.drop_column("user_agents", "webhook_url")
    op.drop_column("user_agents", "webhook_api_key")


def downgrade() -> None:
    # Re-add old columns
    op.add_column("user_agents", sa.Column("webhook_api_key", sa.Text(), nullable=True))
    op.add_column("user_agents", sa.Column("webhook_url", sa.Text(), nullable=True))

    # Migrate data back from webhook_config
    # Only set webhook_url and webhook_api_key for OMNARA_SERVE webhook_type
    op.execute("""
        UPDATE user_agents
        SET webhook_url = webhook_config->>'url',
            webhook_api_key = webhook_config->>'api_key'
        WHERE webhook_type = 'OMNARA_SERVE'
        AND webhook_config IS NOT NULL
        AND webhook_config != '{}'
    """)

    # Drop new columns
    op.drop_column("user_agents", "webhook_config")
    op.drop_column("user_agents", "webhook_type")
