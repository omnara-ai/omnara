-- +goose Up

-- Historical organization creation cannot be inferred from current membership.
-- Only accounts created after this migration are automatically eligible.
ALTER TABLE users ADD COLUMN agent_panel_bootstrap_eligible boolean NOT NULL DEFAULT false;
ALTER TABLE users ALTER COLUMN agent_panel_bootstrap_eligible SET DEFAULT true;

CREATE TABLE agent_panel_preferences (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    agent_id uuid REFERENCES agents(id) ON DELETE SET NULL,
    enabled boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    pending_project_id uuid REFERENCES projects(id) ON DELETE SET NULL
);

CREATE INDEX agent_panel_preferences_agent_idx ON agent_panel_preferences(agent_id)
    WHERE agent_id IS NOT NULL;
CREATE INDEX agent_panel_preferences_pending_project_idx ON agent_panel_preferences(pending_project_id)
    WHERE pending_project_id IS NOT NULL;

-- Provenance is independent of the user's current selection. It supplies
-- request replay, credential teardown and onboarding activity exclusion.
CREATE TABLE assistant_agent_creations (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_id uuid NOT NULL,
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid NOT NULL,
    agent_id uuid NOT NULL UNIQUE,
    personal_access_token_id uuid NOT NULL UNIQUE REFERENCES personal_access_tokens(id),
    secret_id uuid NOT NULL UNIQUE,
    requested_model_id uuid,
    automatic boolean NOT NULL,
    PRIMARY KEY (user_id, request_id),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id),
    FOREIGN KEY (org_id, secret_id) REFERENCES secrets(org_id, id)
);

CREATE INDEX assistant_agent_creations_project_idx ON assistant_agent_creations(org_id, project_id);
