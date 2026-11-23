-- +goose Up

ALTER TABLE model_call_contexts VALIDATE CONSTRAINT model_call_contexts_effective_cost_valid;
