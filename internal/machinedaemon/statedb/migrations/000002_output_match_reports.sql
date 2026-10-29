-- +goose Up

-- watch_process adds the watch action kind. Rebuilding process_actions under
-- a new name before dropping the old table keeps its frozen reports, which
-- would otherwise cascade away with the old rows.
CREATE TABLE process_actions_next (
    action_id TEXT PRIMARY KEY,
    process_id TEXT NOT NULL,
    action_kind TEXT NOT NULL,
    seq INTEGER NOT NULL,
    effect_committed INTEGER NOT NULL,
    CHECK (action_id <> ''),
    CHECK (action_kind IN ('write', 'interrupt', 'terminate', 'watch')),
    CHECK (seq > 0),
    CHECK (effect_committed IN (0, 1)),
    UNIQUE (action_id, process_id),
    UNIQUE (process_id, seq),
    FOREIGN KEY (process_id) REFERENCES processes(process_id) ON DELETE CASCADE
) STRICT;

INSERT INTO process_actions_next(action_id, process_id, action_kind, seq, effect_committed)
SELECT action_id, process_id, action_kind, seq, effect_committed
FROM process_actions;

-- Output-match reports repeat per process, so process report slots gain a
-- sequence: 0 for started and terminal reports, 1 and up for matches.
CREATE TABLE frozen_reports_next (
    report_id TEXT PRIMARY KEY,
    process_id TEXT NOT NULL,
    action_id TEXT,
    report_kind TEXT NOT NULL,
    report_seq INTEGER NOT NULL DEFAULT 0,
    body BLOB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    error_code TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    CHECK (report_id <> '' AND process_id <> ''),
    CHECK (
        report_kind IN (
            'process_started',
            'process_terminal',
            'action_terminal',
            'process_output_match'
        )
    ),
    CHECK ((report_kind = 'process_output_match') = (report_seq > 0)),
    CHECK (report_seq >= 0),
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
    FOREIGN KEY (action_id, process_id) REFERENCES process_actions_next(action_id, process_id) ON DELETE CASCADE
) STRICT;

INSERT INTO frozen_reports_next(
    report_id,
    process_id,
    action_id,
    report_kind,
    body,
    state,
    error_code,
    error
)
SELECT report_id,
       process_id,
       action_id,
       report_kind,
       body,
       state,
       error_code,
       error
FROM frozen_reports;

DROP TABLE frozen_reports;

DROP TABLE process_actions;

-- Renaming also points frozen_reports' foreign key at process_actions.
ALTER TABLE process_actions_next RENAME TO process_actions;

ALTER TABLE frozen_reports_next RENAME TO frozen_reports;

CREATE UNIQUE INDEX frozen_process_report_slot_idx
    ON frozen_reports(process_id, report_kind, report_seq)
    WHERE action_id IS NULL;

CREATE UNIQUE INDEX frozen_action_report_slot_idx
    ON frozen_reports(action_id)
    WHERE action_id IS NOT NULL;

CREATE INDEX frozen_reports_pending_idx
    ON frozen_reports(state, process_id, report_kind);
