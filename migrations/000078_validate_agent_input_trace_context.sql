-- +goose Up

ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_trace_parent_check;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_trace_state_check;
