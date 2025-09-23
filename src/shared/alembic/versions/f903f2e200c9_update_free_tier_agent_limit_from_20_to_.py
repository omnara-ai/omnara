"""Update free tier agent limit from 20 to 10

Revision ID: f903f2e200c9
Revises: 20de0aa419ca
Create Date: 2025-08-09 13:41:40.430740

"""

from typing import Sequence, Union

from alembic import op


# revision identifiers, used by Alembic.
revision: str = "f903f2e200c9"
down_revision: Union[str, None] = "20de0aa419ca"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Update all existing free tier subscriptions from 20 to 10 agents
    op.execute("""
        UPDATE subscriptions
        SET agent_limit = 10
        WHERE plan_type = 'free' AND agent_limit = 20
    """)


def downgrade() -> None:
    # Revert free tier subscriptions back to 20 agents
    op.execute("""
        UPDATE subscriptions
        SET agent_limit = 20
        WHERE plan_type = 'free' AND agent_limit = 10
    """)
