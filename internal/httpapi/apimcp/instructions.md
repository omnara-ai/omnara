Omnara runs durable AI agents. Tools wrap the Omnara REST API (https://docs.omnara.com/api-reference) and act as the authenticated user or org API key.

Resource model: an organization contains projects; agents run inside a project. Machines, machine pools, model providers and models are org-owned; secrets and skills may be org-, project- or user-owned. A project can only use a shared resource after it is granted to it (grant_* tools).

Typical flow:
1. whoami, then orgs_list and projects_list to find the org and project IDs.
2. Check the project's models with grant_models_list; grant one from models_list with grant_models_add if needed. If the agent needs to run commands, give it a machine pool or machine: grant it to the project (grant_pools_add or grant_machines_add) and list it under machine_sources in the config.
3. Start an agent with agents_launch from an agent profile (profiles_list), a saved config (configs_create), or an inline config_source.
4. Follow progress with agents_events_list, send follow-up messages with agents_input, and answer questions or approvals with agents_interactions_list and agents_interactions_resolve.

Pagination: agents_events_list pages by sequence; pass next_after_sequence as after_sequence for newer events, or next_before_sequence as before_sequence for older ones. Other list tools that return next_cursor take it back as cursor. Tools marked destructive (cancel, archive, delete, config updates) change or remove state, so confirm with the user first.
