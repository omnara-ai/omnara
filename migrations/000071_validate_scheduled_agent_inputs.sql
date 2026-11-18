-- +goose Up

ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_state_check;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_lifecycle;
ALTER TABLE agent_inputs VALIDATE CONSTRAINT agent_inputs_scheduled_shape;
