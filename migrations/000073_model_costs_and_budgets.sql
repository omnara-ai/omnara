-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN effective_cost_usd numeric(30, 15),
    ADD COLUMN effective_cost_kind text;

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_effective_cost_valid CHECK (
        (effective_cost_usd IS NULL) = (effective_cost_kind IS NULL)
        AND (effective_cost_usd IS NULL OR (effective_cost_usd <> 'NaN'::numeric AND effective_cost_usd >= 0))
        AND (effective_cost_kind IS NULL OR effective_cost_kind IN ('custom', 'provider_reported', 'estimated'))
        AND (
            effective_cost_kind IS DISTINCT FROM 'provider_reported'
            OR (provider_reported_cost_usd IS NOT NULL AND effective_cost_usd = provider_reported_cost_usd)
        )
        AND (effective_cost_kind IS DISTINCT FROM 'estimated' OR provider_reported_cost_usd IS NULL)
        AND (
            effective_cost_kind NOT IN ('custom', 'estimated')
            OR input_tokens_total IS NOT NULL
            OR output_tokens_total IS NOT NULL
            OR (effective_cost_kind = 'custom' AND effective_cost_usd = 0)
        )
    ) NOT VALID;

ALTER TABLE configured_model_revisions
    ADD COLUMN pricing jsonb CHECK (jsonb_typeof(pricing) = 'object');

CREATE TABLE budgets (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid,
    agent_profile_id uuid,
    agent_id uuid,
    interval text NOT NULL,
    amount_usd numeric(30, 15) NOT NULL,
    created_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    CHECK (interval IN ('day', 'week', 'month', 'lifetime')),
    CHECK (amount_usd <> 'NaN'::numeric AND amount_usd > 0),
    CHECK (num_nonnulls(agent_profile_id, agent_id) <= 1),
    CHECK (project_id IS NOT NULL OR num_nonnulls(agent_profile_id, agent_id) = 0),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id)
);

CREATE UNIQUE INDEX budgets_scope_interval_idx
    ON budgets(org_id, project_id, agent_profile_id, agent_id, interval)
    NULLS NOT DISTINCT;

CREATE TABLE daily_model_costs (
    org_id uuid NOT NULL REFERENCES orgs(id),
    project_id uuid,
    agent_profile_id uuid,
    agent_id uuid,
    day date NOT NULL,
    effective_cost_usd numeric NOT NULL,
    CHECK (
        effective_cost_usd <> 'NaN'::numeric AND effective_cost_usd <> 'Infinity'::numeric AND effective_cost_usd >= 0
    ),
    CHECK (num_nonnulls(agent_profile_id, agent_id) <= 1),
    CHECK (project_id IS NOT NULL OR num_nonnulls(agent_profile_id, agent_id) = 0),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id)
);

CREATE UNIQUE INDEX daily_model_costs_scope_day_idx
    ON daily_model_costs(org_id, project_id, agent_profile_id, agent_id, day)
    NULLS NOT DISTINCT;

CREATE TABLE pending_model_costs (
    model_call_context_id uuid PRIMARY KEY REFERENCES model_call_contexts(id),
    org_id uuid NOT NULL,
    project_id uuid NOT NULL,
    agent_profile_id uuid,
    agent_id uuid NOT NULL,
    day date NOT NULL,
    effective_cost_usd numeric(30, 15) NOT NULL,
    CHECK (effective_cost_usd <> 'NaN'::numeric AND effective_cost_usd >= 0)
);

CREATE INDEX pending_model_costs_org_idx ON pending_model_costs(org_id);

-- +goose StatementBegin
CREATE FUNCTION budget_interval_cost(
    p_org_id uuid, p_project_id uuid, p_agent_profile_id uuid, p_agent_id uuid, p_interval text
)
RETURNS TABLE (effective_cost_usd numeric)
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT coalesce(CASE
        WHEN p_agent_id IS NOT NULL THEN (
            SELECT sum(cost.effective_cost_usd) FROM agents family
            JOIN daily_model_costs cost ON cost.org_id = p_org_id AND cost.project_id = family.project_id
              AND cost.agent_profile_id IS NULL AND cost.agent_id = family.id AND cost.day >= bounds.since
            WHERE family.project_id = p_project_id AND family.root_agent_id = p_agent_id
        )
        WHEN p_agent_profile_id IS NOT NULL THEN (
            SELECT sum(cost.effective_cost_usd) FROM daily_model_costs cost
            WHERE cost.org_id = p_org_id AND cost.project_id = p_project_id
              AND cost.agent_profile_id = p_agent_profile_id AND cost.agent_id IS NULL
              AND cost.day >= bounds.since
        )
        WHEN p_project_id IS NOT NULL THEN (
            SELECT sum(cost.effective_cost_usd) FROM daily_model_costs cost
            WHERE cost.org_id = p_org_id AND cost.project_id = p_project_id
              AND cost.agent_profile_id IS NULL AND cost.agent_id IS NULL AND cost.day >= bounds.since
        )
        ELSE (
            SELECT sum(cost.effective_cost_usd) FROM daily_model_costs cost
            WHERE cost.org_id = p_org_id AND cost.project_id IS NULL
              AND cost.agent_profile_id IS NULL AND cost.agent_id IS NULL AND cost.day >= bounds.since
        )
    END, 0) + coalesce((
        SELECT sum(pending.effective_cost_usd) FROM pending_model_costs pending
        WHERE pending.org_id = p_org_id
          AND (p_project_id IS NULL OR pending.project_id = p_project_id)
          AND (p_agent_profile_id IS NULL OR pending.agent_profile_id = p_agent_profile_id)
          AND (p_agent_id IS NULL OR pending.agent_id IN (
              SELECT family.id FROM agents family
              WHERE family.project_id = p_project_id AND family.root_agent_id = p_agent_id
          ))
          AND pending.day >= bounds.since
    ), 0)
    FROM (
        SELECT CASE p_interval
            WHEN 'day' THEN (statement_timestamp() AT TIME ZONE 'UTC')::date
            WHEN 'week' THEN (date_trunc('week', statement_timestamp(), 'UTC') AT TIME ZONE 'UTC')::date
            WHEN 'month' THEN (date_trunc('month', statement_timestamp(), 'UTC') AT TIME ZONE 'UTC')::date
            ELSE '-infinity'::date
        END AS since
    ) bounds
$$;
-- +goose StatementEnd

CREATE TABLE model_price_catalog (
    singleton_key smallint PRIMARY KEY DEFAULT 1,
    data bytea NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 16777216),
    version text NOT NULL CHECK (version <> ''),
    CHECK (singleton_key = 1)
);
