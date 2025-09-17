"""add instance sharing and teams

Revision ID: 23aa590c6a55
Revises: 9641582c0bf9
Create Date: 2025-09-16 02:29:23.354828

"""

from typing import Sequence, Union

from alembic import op
import sqlalchemy as sa


# revision identifiers, used by Alembic.
revision: str = "23aa590c6a55"
down_revision: Union[str, None] = "9641582c0bf9"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


TEAM_ROLE_ENUM_NAME = "teamrole"
INSTANCE_ACCESS_ENUM_NAME = "instanceaccesslevel"
TEAM_MEMBERSHIP_TEAM_ID_INDEX = "ix_team_memberships_team_id"
USER_INSTANCE_ACCESS_INSTANCE_INDEX = "ix_user_instance_access_instance"
TEAM_INSTANCE_ACCESS_TEAM_INDEX = "ix_team_instance_access_team_id"
TEAM_MEMBERSHIP_TEAM_EMAIL_UNIQUE = "uq_team_memberships_team_email"
TEAM_MEMBERSHIP_TEAM_USER_UNIQUE = "uq_team_memberships_team_user"
USER_INSTANCE_ACCESS_EMAIL_UNIQUE = "uq_user_instance_access_instance_email"
USER_INSTANCE_ACCESS_USER_UNIQUE = "uq_user_instance_access_instance_user"
TEAM_INSTANCE_ACCESS_TEAM_INSTANCE_UNIQUE = "uq_team_instance_access_team_instance"
MESSAGES_SENDER_USER_FK = "fk_messages_sender_user"


team_role_enum = sa.Enum("OWNER", "ADMIN", "MEMBER", name=TEAM_ROLE_ENUM_NAME)
instance_access_enum = sa.Enum("READ", "WRITE", name=INSTANCE_ACCESS_ENUM_NAME)


def upgrade() -> None:
    op.create_table(
        "teams",
        sa.Column("id", sa.UUID(), nullable=False),
        sa.Column("name", sa.String(length=255), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.PrimaryKeyConstraint("id"),
    )

    op.create_table(
        "team_memberships",
        sa.Column("id", sa.UUID(), nullable=False),
        sa.Column("team_id", sa.UUID(), nullable=False),
        sa.Column("user_id", sa.UUID(), nullable=True),
        sa.Column("invited_email", sa.String(length=255), nullable=True),
        sa.Column("role", team_role_enum, nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.ForeignKeyConstraint(
            ["team_id"],
            ["teams.id"],
            name="fk_team_memberships_team_id",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["user_id"],
            ["users.id"],
            name="fk_team_memberships_user_id",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id"),
    )

    op.create_index(
        TEAM_MEMBERSHIP_TEAM_ID_INDEX,
        "team_memberships",
        ["team_id"],
        unique=False,
    )
    op.create_index(
        TEAM_MEMBERSHIP_TEAM_EMAIL_UNIQUE,
        "team_memberships",
        ["team_id", "invited_email"],
        unique=True,
        postgresql_where=sa.text("invited_email IS NOT NULL"),
    )
    op.create_index(
        TEAM_MEMBERSHIP_TEAM_USER_UNIQUE,
        "team_memberships",
        ["team_id", "user_id"],
        unique=True,
        postgresql_where=sa.text("user_id IS NOT NULL"),
    )

    op.create_table(
        "user_instance_access",
        sa.Column("id", sa.UUID(), nullable=False),
        sa.Column("agent_instance_id", sa.UUID(), nullable=False),
        sa.Column("shared_email", sa.String(length=255), nullable=False),
        sa.Column("user_id", sa.UUID(), nullable=True),
        sa.Column("access", instance_access_enum, nullable=False),
        sa.Column("granted_by_user_id", sa.UUID(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.ForeignKeyConstraint(
            ["agent_instance_id"],
            ["agent_instances.id"],
            name="fk_user_instance_access_instance_id",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["granted_by_user_id"],
            ["users.id"],
            name="fk_user_instance_access_granted_by",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["user_id"],
            ["users.id"],
            name="fk_user_instance_access_user_id",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id"),
    )

    op.create_index(
        USER_INSTANCE_ACCESS_INSTANCE_INDEX,
        "user_instance_access",
        ["agent_instance_id"],
        unique=False,
    )
    op.create_index(
        USER_INSTANCE_ACCESS_EMAIL_UNIQUE,
        "user_instance_access",
        ["agent_instance_id", "shared_email"],
        unique=True,
    )
    op.create_index(
        USER_INSTANCE_ACCESS_USER_UNIQUE,
        "user_instance_access",
        ["agent_instance_id", "user_id"],
        unique=True,
        postgresql_where=sa.text("user_id IS NOT NULL"),
    )

    op.create_table(
        "team_instance_access",
        sa.Column("id", sa.UUID(), nullable=False),
        sa.Column("team_id", sa.UUID(), nullable=False),
        sa.Column("agent_instance_id", sa.UUID(), nullable=False),
        sa.Column("access", instance_access_enum.copy(), nullable=False),
        sa.Column("granted_by_user_id", sa.UUID(), nullable=False),
        sa.Column(
            "created_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.Column(
            "updated_at",
            sa.DateTime(timezone=True),
            nullable=False,
            server_default=sa.text("timezone('utc', now())"),
        ),
        sa.ForeignKeyConstraint(
            ["team_id"],
            ["teams.id"],
            name="fk_team_instance_access_team_id",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["agent_instance_id"],
            ["agent_instances.id"],
            name="fk_team_instance_access_instance_id",
            ondelete="CASCADE",
        ),
        sa.ForeignKeyConstraint(
            ["granted_by_user_id"],
            ["users.id"],
            name="fk_team_instance_access_granted_by",
            ondelete="CASCADE",
        ),
        sa.PrimaryKeyConstraint("id"),
    )

    op.create_index(
        TEAM_INSTANCE_ACCESS_TEAM_INDEX,
        "team_instance_access",
        ["team_id"],
        unique=False,
    )
    op.create_index(
        TEAM_INSTANCE_ACCESS_TEAM_INSTANCE_UNIQUE,
        "team_instance_access",
        ["team_id", "agent_instance_id"],
        unique=True,
    )

    op.add_column(
        "messages",
        sa.Column("sender_user_id", sa.UUID(), nullable=True),
    )
    op.create_foreign_key(
        MESSAGES_SENDER_USER_FK,
        "messages",
        "users",
        ["sender_user_id"],
        ["id"],
        ondelete="SET NULL",
    )

    op.execute(
        sa.text(
            """
            UPDATE messages AS m
            SET sender_user_id = ai.user_id
            FROM agent_instances AS ai
            WHERE m.agent_instance_id = ai.id
              AND m.sender_type = 'USER'
              AND m.sender_user_id IS NULL
            """
        )
    )


def downgrade() -> None:
    op.drop_constraint(MESSAGES_SENDER_USER_FK, "messages", type_="foreignkey")
    op.drop_column("messages", "sender_user_id")

    op.drop_index(
        TEAM_INSTANCE_ACCESS_TEAM_INSTANCE_UNIQUE,
        table_name="team_instance_access",
    )
    op.drop_index(
        TEAM_INSTANCE_ACCESS_TEAM_INDEX,
        table_name="team_instance_access",
    )
    op.drop_table("team_instance_access")

    op.drop_index(
        USER_INSTANCE_ACCESS_USER_UNIQUE,
        table_name="user_instance_access",
        postgresql_where=sa.text("user_id IS NOT NULL"),
    )
    op.drop_index(
        USER_INSTANCE_ACCESS_EMAIL_UNIQUE,
        table_name="user_instance_access",
    )
    op.drop_index(
        USER_INSTANCE_ACCESS_INSTANCE_INDEX,
        table_name="user_instance_access",
    )
    op.drop_table("user_instance_access")

    op.drop_index(
        TEAM_MEMBERSHIP_TEAM_USER_UNIQUE,
        table_name="team_memberships",
        postgresql_where=sa.text("user_id IS NOT NULL"),
    )
    op.drop_index(
        TEAM_MEMBERSHIP_TEAM_EMAIL_UNIQUE,
        table_name="team_memberships",
        postgresql_where=sa.text("invited_email IS NOT NULL"),
    )
    op.drop_index(
        TEAM_MEMBERSHIP_TEAM_ID_INDEX,
        table_name="team_memberships",
    )
    op.drop_table("team_memberships")

    op.drop_table("teams")

    bind = op.get_bind()
    instance_access_enum.drop(bind)
    team_role_enum.drop(bind)
