-- +goose Up

ALTER TABLE org_invitations
    DROP CONSTRAINT org_invitations_org_role_check,
    ADD CONSTRAINT org_invitations_org_role_check CHECK (org_role IN ('owner', 'admin', 'member')),
    ADD COLUMN created_by_membership_id uuid,
    ADD CONSTRAINT org_invitations_created_by_membership_fk FOREIGN KEY (org_id, created_by_membership_id)
        REFERENCES org_memberships(org_id, id) ON DELETE CASCADE;

CREATE INDEX org_invitations_created_by_membership_idx
    ON org_invitations(org_id, created_by_membership_id);
