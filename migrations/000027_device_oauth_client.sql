-- +goose Up

ALTER TABLE auth_device_flows
    ADD COLUMN client_id text NOT NULL DEFAULT 'omnara-cli',
    ADD CONSTRAINT auth_device_flows_client_id_check CHECK (
        char_length(client_id) BETWEEN 1 AND 256
        AND client_id !~ '[[:cntrl:]]'
    );
