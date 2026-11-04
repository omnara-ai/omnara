-- +goose NO TRANSACTION
-- +goose Up

DROP INDEX CONCURRENTLY IF EXISTS integration_states_github_check_run_idx;

CREATE INDEX CONCURRENTLY integration_states_github_check_run_idx
    ON integration_states(id)
    WHERE kind = 'github_check_run';
