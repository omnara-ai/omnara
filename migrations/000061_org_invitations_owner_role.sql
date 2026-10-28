-- +goose Up

ALTER TABLE org_invitations
    DROP CONSTRAINT org_invitations_org_role_check,
    ADD CONSTRAINT org_invitations_org_role_check CHECK (org_role IN ('owner', 'admin', 'member'));
