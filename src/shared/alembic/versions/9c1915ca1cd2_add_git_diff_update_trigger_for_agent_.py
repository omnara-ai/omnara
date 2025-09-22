"""Add git_diff update trigger for agent_instances

Revision ID: 9c1915ca1cd2
Revises: f092b4fd6d89
Create Date: 2025-08-06 17:22:29.509777

"""

from typing import Sequence, Union

from alembic import op


# revision identifiers, used by Alembic.
revision: str = "9c1915ca1cd2"
down_revision: Union[str, None] = "f092b4fd6d89"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Update and rename the existing notify_status_change function to handle multiple instance changes
    op.execute("""
        CREATE OR REPLACE FUNCTION notify_instance_change() RETURNS trigger AS $$
        DECLARE
            channel_name text;
            payload text;
        BEGIN
            -- Create channel name based on instance ID
            channel_name := 'message_channel_' || NEW.id::text;

            -- Check if status changed
            IF OLD.status IS DISTINCT FROM NEW.status THEN
                -- Create JSON payload with status update data
                payload := json_build_object(
                    'event_type', 'status_update',
                    'instance_id', NEW.id,
                    'status', NEW.status,
                    'timestamp', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
                )::text;

                -- Send notification (quote channel name for UUIDs with hyphens)
                EXECUTE format('NOTIFY %I, %L', channel_name, payload);
            END IF;

            -- Check if git_diff changed
            IF OLD.git_diff IS DISTINCT FROM NEW.git_diff THEN
                -- Create JSON payload with git_diff update data
                payload := json_build_object(
                    'event_type', 'git_diff_update',
                    'instance_id', NEW.id,
                    'git_diff', NEW.git_diff,
                    'timestamp', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
                )::text;

                -- Send notification (quote channel name for UUIDs with hyphens)
                EXECUTE format('NOTIFY %I, %L', channel_name, payload);
            END IF;

            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)

    # Drop the old function if it exists (for clean migration)
    op.execute("DROP FUNCTION IF EXISTS notify_status_change() CASCADE;")

    # Create new trigger that monitors both status and git_diff columns
    op.execute("""
        CREATE TRIGGER agent_instance_change_notify
        AFTER UPDATE OF status, git_diff ON agent_instances
        FOR EACH ROW
        EXECUTE FUNCTION notify_instance_change();
    """)


def downgrade() -> None:
    # Drop the new trigger and function
    op.execute(
        "DROP TRIGGER IF EXISTS agent_instance_change_notify ON agent_instances;"
    )
    op.execute("DROP FUNCTION IF EXISTS notify_instance_change();")

    # Restore the original notify_status_change function (without git_diff handling)
    op.execute("""
        CREATE OR REPLACE FUNCTION notify_status_change() RETURNS trigger AS $$
        DECLARE
            channel_name text;
            payload text;
        BEGIN
            -- Only notify if status actually changed
            IF OLD.status IS DISTINCT FROM NEW.status THEN
                -- Create channel name based on instance ID
                channel_name := 'message_channel_' || NEW.id::text;

                -- Create JSON payload with status update data
                payload := json_build_object(
                    'event_type', 'status_update',
                    'instance_id', NEW.id,
                    'status', NEW.status,
                    'timestamp', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
                )::text;

                -- Send notification (quote channel name for UUIDs with hyphens)
                EXECUTE format('NOTIFY %I, %L', channel_name, payload);
            END IF;

            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)

    # Recreate original trigger with only status column monitoring
    op.execute("""
        CREATE TRIGGER agent_instance_status_notify
        AFTER UPDATE OF status ON agent_instances
        FOR EACH ROW
        EXECUTE FUNCTION notify_status_change();
    """)
