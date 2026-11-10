-- +goose Up

ALTER TABLE model_call_contexts VALIDATE CONSTRAINT model_call_contexts_estimated_cost_valid;
