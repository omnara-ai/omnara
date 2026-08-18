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
   a. A coding agent for one of my GitHub repos. Ask which repo,
      then check its visibility yourself with an unauthenticated GET
      to https://api.github.com/repos/{owner}/{repo}. Public: no
      credentials needed. 404: confirm the repo name with me — if
      it's private, create a temp file, tell me its path, and have me
      save a fine-grained read-only PAT there. Never have me paste it
      in this chat; I can revoke it anytime.
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
     a. "You are the coding agent for {repo}. Keep a clone of
        the repo on your machine. Answer questions about the code and
        its history, investigate issues, summarize changes, and run
        read-only commands freely." If a PAT was collected, add that
        it has authenticated GitHub access via the GITHUB_TOKEN and
        GH_TOKEN environment variables on its machine.
     b. "You are a research assistant. Search the web, fetch sources,
        and run code on your machine when analysis helps. Cite your
        sources."
   - every built-in tool except create_machine and delete_machine —
     list them from GET /tool-catalog. Include send_integration_message
     and set_integration_target only if I chose Slack; omit both if I
     didn't
   - the granted model and pool
   - if a PAT was collected: create a project-owned secret from the
     temp file without reading or printing its value, delete the
     file, and inject the secret as GITHUB_TOKEN and GH_TOKEN on the
     pool machine_source. Public repos need none of this.

4. Launch an agent from the profile with its first task: for the
   coding agent, clone the repo using git (not the GitHub API) and give
   an orientation summary — recent activity, most active areas, how
   the code is organized; for the research assistant, the topic I
   gave. Stream its events, narrate what it's doing, and show me the
   final reply. Then give me the link to my agent:
   https://app.omnara.com/projects/{project_id}/agents/{agent_id}
   — the conversation lives there; I can keep using it in the browser
   anytime.

5. If I chose Slack: ask for an app configuration token from
   https://api.slack.com/apps (under "Your App Configuration
   Tokens"), call the profile's slack-setup endpoint, and open the
   returned oauth_url in my browser for me to approve within 10
   minutes. Then tell me to @-mention the bot in any channel.
