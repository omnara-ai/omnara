-- +goose Up

ALTER TABLE agent_inputs SET (
    autovacuum_analyze_threshold = 1000,
    autovacuum_analyze_scale_factor = 0.01
);

ALTER TABLE agent_wakeups SET (
    autovacuum_vacuum_threshold = 1000,
    autovacuum_vacuum_scale_factor = 0.01
);

ALTER TABLE agent_runtime_locks SET (
    autovacuum_vacuum_threshold = 1000,
    autovacuum_vacuum_scale_factor = 0.01
);
