-- +goose Up

CREATE TABLE machine_identity (
    singleton INTEGER PRIMARY KEY,
    installation_id TEXT NOT NULL,
    machine_id TEXT NOT NULL,
    CHECK (singleton = 1),
    CHECK (installation_id <> '' AND machine_id <> '')
) STRICT;

CREATE TABLE processes (
    process_id TEXT PRIMARY KEY,
    supervisor_instance_id TEXT NOT NULL,
    supervisor_token TEXT NOT NULL,
    phase TEXT NOT NULL,
    resolved_action_seq INTEGER NOT NULL DEFAULT 0,
    exec_committed INTEGER NOT NULL DEFAULT 0,
    containment_kind TEXT NOT NULL DEFAULT '',
    containment_id TEXT NOT NULL DEFAULT '',
    containment_empty INTEGER NOT NULL DEFAULT 0,
    action_admission_closed INTEGER NOT NULL DEFAULT 0,
    local_closed INTEGER NOT NULL DEFAULT 0,
    server_released INTEGER NOT NULL DEFAULT 0,
    CHECK (
        process_id <> ''
        AND supervisor_instance_id <> ''
        AND supervisor_token <> ''
    ),
    CHECK (phase IN ('preparing', 'prepared', 'accepted', 'terminal')),
    CHECK (resolved_action_seq >= 0),
    CHECK (exec_committed IN (0, 1)),
    CHECK (containment_empty IN (0, 1)),
    CHECK (action_admission_closed IN (0, 1)),
    CHECK (local_closed IN (0, 1)),
    CHECK (server_released IN (0, 1)),
    CHECK (
        (containment_kind = '' AND containment_id = '')
        OR
        (containment_kind <> '' AND containment_id <> '')
    ),
    CHECK (containment_kind = '' OR exec_committed = 1),
    CHECK (phase IN ('accepted', 'terminal') OR exec_committed = 0),
    CHECK (phase <> 'terminal' OR action_admission_closed = 1),
    CHECK (
        local_closed = 0
        OR (
            action_admission_closed = 1
            AND containment_empty = 1
            AND (exec_committed = 0 OR phase = 'terminal')
        )
    )
) STRICT;

CREATE TABLE process_actions (
    action_id TEXT PRIMARY KEY,
    process_id TEXT NOT NULL,
    action_kind TEXT NOT NULL,
    seq INTEGER NOT NULL,
    effect_committed INTEGER NOT NULL,
    CHECK (action_id <> ''),
    CHECK (action_kind IN ('write', 'interrupt', 'terminate')),
    CHECK (seq > 0),
    CHECK (effect_committed IN (0, 1)),
    UNIQUE (action_id, process_id),
    UNIQUE (process_id, seq),
    FOREIGN KEY (process_id) REFERENCES processes(process_id) ON DELETE CASCADE
) STRICT;

CREATE TABLE frozen_reports (
    report_id TEXT PRIMARY KEY,
    process_id TEXT NOT NULL,
    action_id TEXT,
    report_kind TEXT NOT NULL,
    body BLOB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    error_code TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    CHECK (report_id <> '' AND process_id <> ''),
    CHECK (report_kind IN ('process_started', 'process_terminal', 'action_terminal')),
    CHECK (state IN ('pending', 'acknowledged', 'rejected')),
    CHECK (length(body) > 0),
    CHECK (
        (report_kind = 'action_terminal' AND action_id IS NOT NULL)
        OR
        (report_kind <> 'action_terminal' AND action_id IS NULL)
    ),
    CHECK (state <> 'acknowledged' OR action_id IS NULL),
    CHECK (
        (state = 'rejected' AND error_code <> '')
        OR
        (state <> 'rejected' AND error_code = '' AND error = '')
    ),
    FOREIGN KEY (process_id) REFERENCES processes(process_id) ON DELETE CASCADE,
    FOREIGN KEY (action_id, process_id) REFERENCES process_actions(action_id, process_id) ON DELETE CASCADE
) STRICT;

CREATE UNIQUE INDEX frozen_process_report_slot_idx
    ON frozen_reports(process_id, report_kind)
    WHERE action_id IS NULL;

CREATE UNIQUE INDEX frozen_action_report_slot_idx
    ON frozen_reports(action_id)
    WHERE action_id IS NOT NULL;

CREATE INDEX frozen_reports_pending_idx
    ON frozen_reports(state, process_id, report_kind);
