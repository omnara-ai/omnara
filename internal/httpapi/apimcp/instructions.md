Omnara runs durable AI agents. Tools wrap the Omnara REST API (https://docs.omnara.com/api-reference) and act as the authenticated user or org API key.

Resource model: an organization contains projects; agents run inside a project. Machines, machine pools, model providers and models are org-owned; secrets and skills may be org-, project- or user-owned. A project can only use a shared resource after it is granted to it (grant_* tools).

Typical flow:
1. whoami, then orgs_list and projects_list to find the org and project IDs.
2. Make sure the project has a model (models_list, grant_models_add) and somewhere to run (grant_pools_add or grant_machines_add).
3. Start an agent with agents_launch from an agent profile (profiles_list), a saved config (configs_create), or an inline config_source.
4. Follow progress with agents_events_list, send follow-up messages with agents_input, and answer questions or approvals with agents_interactions_list and agents_interactions_resolve.

List tools paginate: pass a response's next_cursor as cursor to get the next page. Tools marked destructive (cancel, archive, delete, config updates) change or remove state, so confirm with the user first.
