-- +goose Up

CREATE TABLE oauth_authorization_codes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    code_hash text NOT NULL,
    user_id uuid NOT NULL REFERENCES users(id),
    client_id text NOT NULL,
    client_name text NOT NULL,
    redirect_uri text NOT NULL,
    code_challenge text NOT NULL,
    resource text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    UNIQUE (code_hash),
    CHECK (code_hash <> ''),
    CHECK (octet_length(client_id) BETWEEN 1 AND 2048 AND (client_id COLLATE "C") !~ '[[:cntrl:]]'),
    CHECK (char_length(client_name) BETWEEN 1 AND 128 AND client_name !~ '[[:cntrl:]]'),
    CHECK (octet_length(redirect_uri) BETWEEN 1 AND 2048 AND (redirect_uri COLLATE "C") !~ '[[:cntrl:]]'),
    CHECK (octet_length(code_challenge) BETWEEN 43 AND 128),
    CHECK (octet_length(resource) BETWEEN 1 AND 2048 AND (resource COLLATE "C") !~ '[[:cntrl:]]'),
    CHECK (expires_at > created_at),
    CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

CREATE INDEX oauth_authorization_codes_cleanup_idx
    ON oauth_authorization_codes(expires_at, id);

CREATE TABLE oauth_access_tokens (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES users(id),
    client_id text NOT NULL,
    client_name text NOT NULL,
    resource text NOT NULL,
    token_hash text NOT NULL,
    refresh_token_hash text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    refresh_expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    UNIQUE (token_hash),
    UNIQUE (refresh_token_hash),
    CHECK (octet_length(client_id) BETWEEN 1 AND 2048 AND (client_id COLLATE "C") !~ '[[:cntrl:]]'),
    CHECK (char_length(client_name) BETWEEN 1 AND 128 AND client_name !~ '[[:cntrl:]]'),
    CHECK (octet_length(resource) BETWEEN 1 AND 2048 AND (resource COLLATE "C") !~ '[[:cntrl:]]'),
    CHECK (token_hash <> ''),
    CHECK (refresh_token_hash <> ''),
    CHECK (expires_at > created_at),
    CHECK (refresh_expires_at >= expires_at),
    CHECK (last_used_at IS NULL OR last_used_at >= created_at),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX oauth_access_tokens_active_user_idx
    ON oauth_access_tokens(user_id)
    WHERE revoked_at IS NULL;

CREATE INDEX oauth_access_tokens_cleanup_idx
    ON oauth_access_tokens(refresh_expires_at, id);

CREATE TABLE oauth_retired_refresh_tokens (
    refresh_token_hash text PRIMARY KEY,
    oauth_access_token_id uuid NOT NULL REFERENCES oauth_access_tokens(id) ON DELETE CASCADE,
    retired_at timestamptz NOT NULL,
    CHECK (refresh_token_hash <> '')
);

CREATE INDEX oauth_retired_refresh_tokens_token_idx
    ON oauth_retired_refresh_tokens(oauth_access_token_id, retired_at DESC);
