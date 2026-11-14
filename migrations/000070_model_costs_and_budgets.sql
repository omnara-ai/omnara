-- +goose Up

ALTER TABLE model_call_contexts
    ADD COLUMN effective_cost_usd numeric(30, 15),
    ADD COLUMN effective_cost_kind text;

ALTER TABLE model_call_contexts
    ADD CONSTRAINT model_call_contexts_effective_cost_valid CHECK (
        (effective_cost_usd IS NULL) = (effective_cost_kind IS NULL)
        AND (effective_cost_usd IS NULL OR (effective_cost_usd <> 'NaN'::numeric AND effective_cost_usd >= 0))
        AND (effective_cost_kind IS NULL OR effective_cost_kind IN ('custom', 'provider_reported', 'estimated'))
        AND (effective_cost_kind IS DISTINCT FROM 'provider_reported' OR effective_cost_usd = provider_reported_cost_usd)
        AND (effective_cost_kind IS DISTINCT FROM 'estimated' OR provider_reported_cost_usd IS NULL)
        AND (
            effective_cost_kind NOT IN ('custom', 'estimated')
            OR input_tokens_total IS NOT NULL
            OR output_tokens_total IS NOT NULL
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
    deleted_at timestamptz,
    CHECK (interval IN ('day', 'week', 'month', 'lifetime')),
    CHECK (amount_usd <> 'NaN'::numeric AND amount_usd > 0),
    CHECK (num_nonnulls(agent_profile_id, agent_id) <= 1),
    CHECK (project_id IS NOT NULL OR num_nonnulls(agent_profile_id, agent_id) = 0),
    FOREIGN KEY (org_id, project_id) REFERENCES projects(org_id, id),
    FOREIGN KEY (project_id, agent_profile_id) REFERENCES agent_profiles(project_id, id),
    FOREIGN KEY (project_id, agent_id) REFERENCES agents(project_id, id)
);

CREATE UNIQUE INDEX budgets_active_scope_interval_idx
    ON budgets(org_id, project_id, agent_profile_id, agent_id, interval)
    NULLS NOT DISTINCT WHERE deleted_at IS NULL;

CREATE TABLE daily_model_costs (
    org_id uuid NOT NULL,
    day date NOT NULL,
    project_id uuid NOT NULL,
    agent_profile_id uuid,
    model_call_context_id uuid,
    effective_cost_usd numeric(30, 15) NOT NULL,
    CHECK (effective_cost_usd <> 'NaN'::numeric AND effective_cost_usd >= 0)
);

CREATE UNIQUE INDEX daily_model_costs_day_call_idx
    ON daily_model_costs(org_id, day, project_id, agent_profile_id, model_call_context_id)
    NULLS NOT DISTINCT;

CREATE INDEX daily_model_costs_unmerged_idx
    ON daily_model_costs(org_id, model_call_context_id) WHERE model_call_context_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION budget_interval_cost(
    p_org_id uuid, p_project_id uuid, p_agent_profile_id uuid, p_agent_id uuid, p_interval text
)
RETURNS TABLE (effective_cost_usd numeric)
LANGUAGE sql STABLE PARALLEL SAFE
AS $$
    SELECT coalesce(CASE WHEN p_agent_id IS NULL THEN (
        SELECT sum(cost.effective_cost_usd)
        FROM daily_model_costs cost
        WHERE cost.org_id = p_org_id
          AND (p_project_id IS NULL OR cost.project_id = p_project_id)
          AND (p_agent_profile_id IS NULL OR cost.agent_profile_id = p_agent_profile_id)
          AND cost.day >= (bounds.since AT TIME ZONE 'UTC')::date
    ) ELSE (
        SELECT sum(call.effective_cost_usd)
        FROM agents agent
        JOIN model_call_contexts call ON call.project_id = agent.project_id AND call.agent_id = agent.id
        WHERE agent.project_id = p_project_id
          AND agent.root_agent_id = p_agent_id
          AND call.effective_cost_usd IS NOT NULL
          AND call.created_at >= bounds.since
    ) END, 0)
    FROM (
        SELECT CASE p_interval
            WHEN 'day' THEN date_trunc('day', statement_timestamp(), 'UTC')
            WHEN 'week' THEN date_trunc('week', statement_timestamp(), 'UTC')
            WHEN 'month' THEN date_trunc('month', statement_timestamp(), 'UTC')
            ELSE '-infinity'::timestamptz
        END AS since
    ) bounds
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION sync_daily_model_cost_from_model_call_context() RETURNS trigger AS $$
BEGIN
    INSERT INTO daily_model_costs(
        org_id, day, project_id, agent_profile_id, model_call_context_id, effective_cost_usd
    ) VALUES (
        NEW.org_id,
        (NEW.created_at AT TIME ZONE 'UTC')::date,
        NEW.project_id,
        (
            SELECT root.agent_profile_id
            FROM agents agent
            JOIN agents root ON root.project_id = agent.project_id AND root.id = agent.root_agent_id
            WHERE agent.project_id = NEW.project_id AND agent.id = NEW.agent_id
        ),
        NEW.id,
        NEW.effective_cost_usd
    );
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER model_call_contexts_insert_daily_model_cost
    AFTER UPDATE OF state
    ON model_call_contexts
    FOR EACH ROW
    WHEN (
        OLD.state = 'started'
        AND NEW.state <> 'started'
        AND NEW.effective_cost_usd IS NOT NULL
    )
    EXECUTE FUNCTION sync_daily_model_cost_from_model_call_context();
