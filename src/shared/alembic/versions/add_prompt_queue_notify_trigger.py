"""Add prompt queue notify trigger

Revision ID: queue_prompts_002
Revises: queue_prompts_001
Create Date: 2025-11-10 02:00:00.000000

"""

from typing import Sequence, Union

from alembic import op

# revision identifiers, used by Alembic.
revision: str = "queue_prompts_002"
down_revision: Union[str, None] = "queue_prompts_001"
branch_labels: Union[str, Sequence[str], None] = None
depends_on: Union[str, Sequence[str], None] = None


def upgrade() -> None:
    # Create function to notify queue changes
    op.execute("""
        CREATE OR REPLACE FUNCTION notify_queue_change() RETURNS trigger AS $$
        DECLARE
            channel_name text;
            payload text;
            event_type text;
            operation_type text;
        BEGIN
            -- Determine operation type
            IF TG_OP = 'INSERT' THEN
                operation_type := 'insert';
            ELSIF TG_OP = 'UPDATE' THEN
                operation_type := 'update';
            ELSIF TG_OP = 'DELETE' THEN
                operation_type := 'delete';
            END IF;

            -- Use the instance ID from the row (NEW for INSERT/UPDATE, OLD for DELETE)
            IF TG_OP = 'DELETE' THEN
                channel_name := 'message_channel_' || OLD.agent_instance_id::text;
            ELSE
                channel_name := 'message_channel_' || NEW.agent_instance_id::text;
            END IF;

            -- Create JSON payload with queue update data
            payload := json_build_object(
                'event_type', 'queue_update',
                'operation', operation_type,
                'agent_instance_id', CASE
                    WHEN TG_OP = 'DELETE' THEN OLD.agent_instance_id
                    ELSE NEW.agent_instance_id
                END,
                'queue_item_id', CASE
                    WHEN TG_OP = 'DELETE' THEN OLD.id
                    ELSE NEW.id
                END,
                'timestamp', to_char(NOW() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
            )::text;

            -- Send notification (quote channel name for UUIDs with hyphens)
            EXECUTE format('NOTIFY %I, %L', channel_name, payload);

            IF TG_OP = 'DELETE' THEN
                RETURN OLD;
            ELSE
                RETURN NEW;
            END IF;
        END;
        $$ LANGUAGE plpgsql;
    """)

    # Create trigger on prompt_queue table for INSERT, UPDATE, and DELETE
    op.execute("""
        CREATE TRIGGER prompt_queue_change_notify
        AFTER INSERT OR UPDATE OR DELETE ON prompt_queue
        FOR EACH ROW
        EXECUTE FUNCTION notify_queue_change();
    """)


def downgrade() -> None:
    # Drop trigger
    op.execute("DROP TRIGGER IF EXISTS prompt_queue_change_notify ON prompt_queue;")

    # Drop function
    op.execute("DROP FUNCTION IF EXISTS notify_queue_change();")
