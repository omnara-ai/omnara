-- +goose Up

ALTER TABLE secrets DROP CONSTRAINT secrets_kind_check;
ALTER TABLE secrets ADD CONSTRAINT secrets_kind_check
    CHECK (kind IN ('generic', 'oauth_token_set', 'slack_app_credentials', 'aws_credentials'));
