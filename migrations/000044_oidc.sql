-- +goose Up

ALTER TABLE oauth_authorization_codes
    ADD COLUMN scope text NOT NULL DEFAULT '',
    ADD COLUMN nonce text NOT NULL DEFAULT '';
ALTER TABLE oauth_access_tokens
    ADD COLUMN granted_scope text NOT NULL DEFAULT '',
    ADD COLUMN scope text NOT NULL DEFAULT '';

CREATE TABLE oidc_signing_keys (
    id text PRIMARY KEY CHECK (id = 'default'),
    encrypted_private_key jsonb NOT NULL
);
