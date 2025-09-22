"""Fix large payload notifications

Revision ID: 9fe045ea7ad9
Revises: 9c1915ca1cd2
Create Date: 2025-08-06 21:34:06.523760

"""

from typing import Sequence, Union

from alembic import op


# revision identifiers, used by Alembic.
revision: str = "9fe045ea7ad9"
down_revision: Union[str, None] = "9c1915ca1cd2"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Update notify_message_change to send lightweight notifications without content
    op.execute("""
        CREATE OR REPLACE FUNCTION notify_message_change() RETURNS trigger AS $$
        DECLARE
            channel_name text;
            payload text;
            event_type text;
        BEGIN
            -- Create channel name based on instance ID
            channel_name := 'message_channel_' || NEW.agent_instance_id::text;

            -- Determine event type
            IF TG_OP = 'INSERT' THEN
                event_type := 'message_insert';
            ELSIF TG_OP = 'UPDATE' THEN
                event_type := 'message_update';
            END IF;

            -- Create JSON payload WITHOUT the actual content (lightweight notification)
            payload := json_build_object(
                'event_type', event_type,
                'id', NEW.id,
                'agent_instance_id', NEW.agent_instance_id,
                'sender_type', NEW.sender_type,
                'created_at', to_char(NEW.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
                'requires_user_input', NEW.requires_user_input,
                'old_requires_user_input', CASE
                    WHEN TG_OP = 'UPDATE' THEN OLD.requires_user_input
                    ELSE NULL
                END
            )::text;

            -- Send notification (quote channel name for UUIDs with hyphens)
            EXECUTE format('NOTIFY %I, %L', channel_name, payload);

            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)

    # Update notify_instance_change to send lightweight git_diff notifications
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
                -- Create JSON payload with status update data (keep as-is, already lightweight)
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
                -- Create JSON payload WITHOUT the actual git_diff (lightweight notification)
                payload := json_build_object(
                    'event_type', 'git_diff_update',
                    'instance_id', NEW.id,
                    'timestamp', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
                )::text;

                -- Send notification (quote channel name for UUIDs with hyphens)
                EXECUTE format('NOTIFY %I, %L', channel_name, payload);
            END IF;

            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)


def downgrade() -> None:
    # Restore original notify_message_change with full content
    op.execute("""
        CREATE OR REPLACE FUNCTION notify_message_change() RETURNS trigger AS $$
        DECLARE
            channel_name text;
            payload text;
            event_type text;
        BEGIN
            -- Create channel name based on instance ID
            channel_name := 'message_channel_' || NEW.agent_instance_id::text;

            -- Determine event type
            IF TG_OP = 'INSERT' THEN
                event_type := 'message_insert';
            ELSIF TG_OP = 'UPDATE' THEN
                event_type := 'message_update';
            END IF;

            -- Create JSON payload with message data (original version with content)
            payload := json_build_object(
                'event_type', event_type,
                'id', NEW.id,
                'agent_instance_id', NEW.agent_instance_id,
                'sender_type', NEW.sender_type,
                'content', NEW.content,
                'created_at', to_char(NEW.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
                'requires_user_input', NEW.requires_user_input,
                'message_metadata', NEW.message_metadata,
                'old_requires_user_input', CASE
                    WHEN TG_OP = 'UPDATE' THEN OLD.requires_user_input
                    ELSE NULL
                END
            )::text;

            -- Send notification (quote channel name for UUIDs with hyphens)
            EXECUTE format('NOTIFY %I, %L', channel_name, payload);

            RETURN NEW;
        END;
        $$ LANGUAGE plpgsql;
    """)

    # Restore original notify_instance_change with full git_diff
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
                -- Create JSON payload with git_diff update data (original with full diff)
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
