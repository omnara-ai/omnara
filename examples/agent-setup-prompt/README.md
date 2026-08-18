Set up Omnara for me (an API for durable agents). Read
https://docs.omnara.com/llms.txt and the Quickstart first; pull other
doc pages as you need them. API base: https://api.omnara.com/v1.
Narrate each step briefly and show your API calls. If a call fails,
read the error (the `code` field is the contract), fix the request,
and retry — don't skip ahead.

1. Ask me two questions before creating anything:
   - Connect the agent to Slack?
   - Give it GitHub access? If yes: create a temp file, tell me its
     path, and have me save a fine-grained read-only PAT there — never paste it in this chat. Also ask which repo to demo on.

2. Authenticate me with device login (docs: Authentication). Send me
   the approval link — signup and approval happen in the same visit —
   and poll until you have my API key. Save it to ~/.omnara/token,
   readable only by me, and never print it. Tell me it's there for
   future use.

3. Discover my org, its default project, the granted model, and the
   granted machine pool via the API — everything happens in the
   default project. Use exact names from the responses. If the model
   or pool is missing, stop and tell me.

4. Create an agent profile named "Assistant":
   - instruction: "You are a general-purpose assistant." If I chose
     GitHub, add that it has authenticated GitHub access via the
     GITHUB_TOKEN and GH_TOKEN environment variables on its machine.
   - every built-in tool except create_machine and delete_machine —
     list them from GET /tool-catalog. Include send_integration_message
     and set_integration_target only if I chose Slack; omit both if I
     didn't
   - the granted model and pool
   - if GitHub: create a project-owned secret by piping the temp file
     into the create-secret call without reading or printing its
     contents, using whatever JSON tool is available (jq, python3,
     node), then delete the temp file. Inject the secret as
     GITHUB_TOKEN and GH_TOKEN on the pool machine_source. My token
     goes straight from the file to Omnara's encrypted secret store —
     you never see it.

5. Launch an agent from the profile. First task: if GitHub, clone my
   repo and summarize recent activity; otherwise research a current
   topic. Either way it must first ask me one clarifying question via
   ask_question. Stream its events, relay the question, resolve it
   with my answer, and show me the final reply.

6. If I chose Slack: ask for an app configuration token from
   https://api.slack.com/apps (under "Your App Configuration Tokens"),
   call the profile's slack-setup endpoint, and send me the oauth_url
   to approve within 10 minutes.

7. Finish with the link to my live agent:
   https://app.omnara.com/projects/{project_id}/agents/{agent_id}
   — I can watch and reply there too.
