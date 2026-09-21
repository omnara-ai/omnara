Set up Omnara for me (an API for durable agents). Read
https://docs.omnara.com/llms.txt and the Quickstart first; pull other
doc pages as you need them. API base: https://api.omnara.com/v1.
Narrate each step briefly and show your API calls. If a call fails,
read the error (the `code` field is the contract), fix the request,
and retry — don't skip ahead.

1. Ask me two questions before creating anything. First: should the
   agent be connected to Slack so my team can use it too? (The
   connection happens at the end.) Second: which agent should I set
   up? This will be a real agent I keep using, not a throwaway.
   a. A coding agent. Ask which GitHub repo it should start with,
      then check its visibility yourself with an unauthenticated GET
      to https://api.github.com/repos/{owner}/{repo}. Public: no
      credentials needed. 404: confirm the repo name with me — if
      it's private, create a temp file, tell me its path, and have me
      save a GitHub PAT there.
   b. A research assistant that searches the web and runs code on its
      own machine. No credentials needed. Ask what it should research
      first.
   Or I can describe my own use case and you tailor the instruction —
   same web + machine tools either way.

2. Authenticate me with device login (docs: Authentication). Open the
   approval link in my browser (open / xdg-open / start) and also
   print it in case it doesn't open — signup and approval happen in
   the same visit — then poll until you have my API key. Save it to
   ~/.omnara/token, readable only by me, and never print it.

3. Look up my org, its default project, and the granted model and
   machine pool via the API — everything happens in the default
   project, using exact names from the responses. If the model or
   pool is missing, stop and tell me. Then create an agent profile
   named for my choice ("Coding Agent" / "Research Assistant"):
   - instruction, per my choice:
     a. "You are a coding agent with your own machine. Start with
        {repo}: keep a clone of it, answer questions and work with 
        other repos when asked." If a PAT was collected, add that
        it has authenticated GitHub access via the GITHUB_TOKEN and
        GH_TOKEN environment variables on its machine.
     b. "You are a research assistant. Search the web, fetch sources,
        and run code on your machine when analysis helps. Cite your
        sources."
   - ordinary built-in tools except create_machine and delete_machine —
     list them from GET /tool-catalog. The compiler supplies
     list_interaction_handlers and set_interaction_handler by default. If I
     chose Slack, its app launcher below supplies namespaced read/send tools,
     an app-owned thread subscription and an interaction handler to agents it launches;
     leave app tools out of the base profile. Tell the agent to use the
     available app__<app-name>__post_message tool for answers and updates
     in Slack, or reply directly in Omnara when no Slack tool is available
   - the granted model and pool
   - if a PAT was collected: create a project-owned secret from the
     temp file without reading or printing its value, delete the
     file, and inject the secret as GITHUB_TOKEN and GH_TOKEN on the
     pool machine_source. Public repos need none of this.

4. Launch an agent from the profile with a simple first task — the
   goal is a quick, clear first reply, not a comprehensive report:
   for the coding agent, clone the repo using git (not the GitHub
   API) and tell me the latest commit and who made it; for the research assistant, the topic I gave, answered concisely. Stream
   its events, narrate what it's doing, and show me the final reply. Then give me the link to my agent:
   https://app.omnara.com/projects/{project_id}/agents/{agent_id}
   — the conversation lives there; I can keep using it in the browser
   anytime.

5. If I chose Slack: ask for the Slack workspace ID (T…) and an app
   configuration token from https://api.slack.com/apps (under "Your App
   Configuration Tokens"). Create a project app with an immutable name
   such as "team-chat", definition_id "omnara.slack", and
   settings.launcher {trigger: "mention", scope_kind: "workspace",
   scope_ref: <workspace ID>, slots: [{key: "default",
   agent_profile_id: <profile ID>}]}. Use the returned app ID with
   POST /orgs/{orgID}/projects/{projectID}/apps/{appID}/slack-setup, sending
   app_name and app_configuration_token. Open the returned oauth_url in my
   browser for me to approve before expires_at. Once approved, tell me to
   invite the bot to a channel in that workspace and @-mention it to start a
   conversation, or DM it for a persistent one-on-one agent.

For an existing native Slack deployment, use the coordinated maintenance cutover
at https://docs.omnara.com/self-hosting/composable-apps-cutover before this setup.
It preserves prior sending scopes as app-agent context and does not create
subscriptions or interaction handlers for old conversations.
