"""Add name field to agent_instances

Revision ID: add_name_field_2024
Revises: f1a2b3c4d5e6
Create Date: 2024-01-01 00:00:00.000000

"""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


# revision identifiers, used by Alembic.
revision: str = "add_name_field_2024"
down_revision: Union[str, None] = "f1a2b3c4d5e6"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Add name column to agent_instances table
    op.add_column("agent_instances", sa.Column("name", sa.String(255), nullable=True))


def downgrade() -> None:
    # Remove name column from agent_instances table
    op.drop_column("agent_instances", "name")
