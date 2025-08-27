# Omnara Integration Changes for Claude Code Action v1.0

This document details all changes made to support Omnara's repository_dispatch workflow, so they can be applied to the v1.0 version of Claude Code Action.

## Overview

The Omnara integration allows triggering Claude Code from the Omnara dashboard via GitHub's repository_dispatch API. The key change is replacing the direct Claude Code CLI execution with `omnara headless` command, which wraps Claude Code and provides session tracking.

## 1. Repository Dispatch Event Support

### File: `src/github/context.ts`

**Add repository_dispatch event type definition:**

```typescript
export type RepositoryDispatchEvent = {
  action?: string;
  client_payload?: Record<string, any>;
  repository: {
    name: string;
    owner: {
      login: string;
    };
  };
  sender?: {
    login: string;
  };
};

export type GitHubEvent = 
  | IssueEvent
  | PullRequestEvent
  | IssueCommentEvent
  | PullRequestReviewCommentEvent
  | PullRequestReviewEvent
  | WorkflowDispatchEvent
  | ScheduleEvent
  | RepositoryDispatchEvent  // ADD THIS
  | PushEvent
  | WorkflowRunEvent;
```

**Update context parsing to handle repository_dispatch:**

```typescript
function parseRepositoryDispatchContext(
  event: RepositoryDispatchEvent,
): RepositoryDispatchContext {
  return {
    eventName: "repository_dispatch",
    eventType: event.action || "unknown",
    repository: {
      owner: event.repository.owner.login,
      name: event.repository.name,
    },
    sender: event.sender?.login || "unknown",
    clientPayload: event.client_payload || {},
  };
}

// In parseGitHubContext function, add:
case "repository_dispatch":
  return parseRepositoryDispatchContext(
    event as RepositoryDispatchEvent,
  );
```

### File: `src/modes/agent/index.ts`

**Ensure agent mode supports repository_dispatch:**

```typescript
shouldTrigger(context: GitHubContext): boolean {
  // Agent mode should trigger for repository_dispatch
  return (
    context.eventName === "workflow_dispatch" ||
    context.eventName === "schedule" ||
    context.eventName === "repository_dispatch"  // ADD THIS
  );
}

// In prepareContext method, handle repository_dispatch:
if (context.eventName === "repository_dispatch") {
  const dispatchContext = context as RepositoryDispatchContext;
  return {
    prompt: dispatchContext.clientPayload.prompt || "",
    // Other fields...
  };
}
```

## 2. Omnara Headless Integration

### File: `base-action/src/run-omnara.ts` (NEW FILE)

This is the core integration that replaces `run-claude.ts`. Instead of running Claude Code directly, it runs `omnara headless` which wraps Claude Code with session tracking:

```typescript
import * as core from "@actions/core";
import { spawn } from "child_process";
import { writeFile, stat, readFile } from "fs/promises";

const EXECUTION_FILE = `${process.env.RUNNER_TEMP}/omnara-execution-output.json`;

export type OmnaraOptions = {
  allowedTools?: string;
  disallowedTools?: string;
  maxTurns?: string;
  mcpConfig?: string;
  systemPrompt?: string;
  appendSystemPrompt?: string;
  claudeEnv?: string;
  fallbackModel?: string;
  timeoutMinutes?: string;
  model?: string;
};

export async function runOmnara(promptPath: string, options: OmnaraOptions) {
  // Build omnara headless arguments
  const omnaraArgs = ["headless"];
  
  // Read prompt content from file
  const promptContent = await readFile(promptPath, 'utf8');
  if (promptContent) {
    omnaraArgs.push("--prompt", promptContent);
  }

  // Session ID from environment or generate
  const sessionId = process.env.OMNARA_AGENT_INSTANCE_ID || `gh-action-${Date.now()}`;
  omnaraArgs.push("--session-id", sessionId);
  
  // Agent name/type from environment
  const agentType = process.env.OMNARA_AGENT_TYPE || "GitHub Action";
  omnaraArgs.push("--name", agentType);

  // Tool configurations
  if (options.allowedTools) {
    omnaraArgs.push("--allowed-tools", options.allowedTools);
  }
  if (options.disallowedTools) {
    omnaraArgs.push("--disallowed-tools", options.disallowedTools);
  }

  // Max turns
  if (options.maxTurns) {
    omnaraArgs.push("--max-turns", options.maxTurns);
  }

  console.log(`Running Omnara with command: omnara ${omnaraArgs.join(' ')}`);

  // Environment setup - CRITICAL: Pass through all authentication
  const omnaraEnv = {
    ...process.env,
    // Omnara configuration
    OMNARA_API_KEY: process.env.OMNARA_API_KEY,
    OMNARA_AGENT_INSTANCE_ID: process.env.OMNARA_AGENT_INSTANCE_ID,
    OMNARA_AGENT_TYPE: process.env.OMNARA_AGENT_TYPE,
    // Claude authentication (omnara uses these internally)
    ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY,
    CLAUDE_CODE_OAUTH_TOKEN: process.env.CLAUDE_CODE_OAUTH_TOKEN,
    // Cloud provider credentials if using Bedrock/Vertex
    CLAUDE_CODE_USE_BEDROCK: process.env.CLAUDE_CODE_USE_BEDROCK,
    CLAUDE_CODE_USE_VERTEX: process.env.CLAUDE_CODE_USE_VERTEX,
    AWS_REGION: process.env.AWS_REGION,
    AWS_ACCESS_KEY_ID: process.env.AWS_ACCESS_KEY_ID,
    AWS_SECRET_ACCESS_KEY: process.env.AWS_SECRET_ACCESS_KEY,
    AWS_SESSION_TOKEN: process.env.AWS_SESSION_TOKEN,
    ANTHROPIC_BEDROCK_BASE_URL: process.env.ANTHROPIC_BEDROCK_BASE_URL,
    ANTHROPIC_VERTEX_PROJECT_ID: process.env.ANTHROPIC_VERTEX_PROJECT_ID,
    CLOUD_ML_REGION: process.env.CLOUD_ML_REGION,
    GOOGLE_APPLICATION_CREDENTIALS: process.env.GOOGLE_APPLICATION_CREDENTIALS,
    ANTHROPIC_VERTEX_BASE_URL: process.env.ANTHROPIC_VERTEX_BASE_URL,
  };
  
  const omnaraProcess = spawn("omnara", omnaraArgs, {
    stdio: ["inherit", "pipe", "inherit"],
    env: omnaraEnv,
  });

  // Capture and process output
  let output = "";
  omnaraProcess.stdout.on("data", (data) => {
    const text = data.toString();
    // Pretty print JSON if detected
    const lines = text.split("\n");
    lines.forEach((line: string) => {
      if (line.trim() === "") return;
      try {
        const parsed = JSON.parse(line);
        const prettyJson = JSON.stringify(parsed, null, 2);
        process.stdout.write(prettyJson + "\n");
      } catch (e) {
        process.stdout.write(line + "\n");
      }
    });
    output += text;
  });

  // Wait for completion with timeout
  await new Promise<void>((resolve, reject) => {
    const timeoutMs = parseInt(options.timeoutMinutes || "10") * 60 * 1000;
    const timeout = setTimeout(() => {
      omnaraProcess.kill("SIGTERM");
      reject(new Error(`Command timed out after ${timeoutMs / 1000 / 60} minutes`));
    }, timeoutMs);

    omnaraProcess.on("exit", (code) => {
      clearTimeout(timeout);
      if (code === 0) {
        resolve();
      } else {
        reject(new Error(`Omnara exited with code ${code}`));
      }
    });
  });

  // Save output for GitHub Actions
  await writeFile(EXECUTION_FILE, output);
  core.setOutput("execution_file", EXECUTION_FILE);
  core.setOutput("conclusion", "success");
}
```

### File: `base-action/src/index.ts`

**Replace Claude Code execution with Omnara:**

```typescript
import { runOmnara } from "./run-omnara";  // Instead of run-claude

async function run() {
  try {
    // ... existing setup code ...

    // Replace runClaude with runOmnara
    await runOmnara(promptConfig.path, {
      allowedTools: process.env.INPUT_ALLOWED_TOOLS,
      disallowedTools: process.env.INPUT_DISALLOWED_TOOLS,
      maxTurns: process.env.INPUT_MAX_TURNS,
      mcpConfig: process.env.INPUT_MCP_CONFIG,
      systemPrompt: process.env.INPUT_SYSTEM_PROMPT,
      appendSystemPrompt: process.env.INPUT_APPEND_SYSTEM_PROMPT,
      claudeEnv: process.env.INPUT_CLAUDE_ENV,
      fallbackModel: process.env.INPUT_FALLBACK_MODEL,
      model: process.env.ANTHROPIC_MODEL,
    });
  } catch (error) {
    core.setFailed(`Action failed with error: ${error}`);
    core.setOutput("conclusion", "failure");
    process.exit(1);
  }
}
```

## 3. Action Definition Changes

### File: `action.yml`

**Add Omnara installation step:**

```yaml
steps:
  # ... existing steps ...

  - name: Install Claude Code and Omnara
    if: steps.prepare.outputs.contains_trigger == 'true'
    shell: bash
    run: |
      echo "Installing Claude Code CLI..."
      # Install Claude Code globally (required by omnara headless)
      curl -fsSL https://claude.ai/install.sh | bash -s 1.0.89
      echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      
      echo "Installing Omnara package..."
      python3 -m pip install --upgrade pip
      pip install omnara
      echo "Omnara package installed"

  - name: Validate Omnara Configuration
    if: steps.prepare.outputs.contains_trigger == 'true'
    shell: bash
    run: |
      if [ -z "${OMNARA_API_KEY}" ]; then
        echo "Error: OMNARA_API_KEY environment variable is required"
        exit 1
      fi

  - name: Run Omnara Headless
    id: claude-code
    if: steps.prepare.outputs.contains_trigger == 'true'
    shell: bash
    run: |
      bun run ${GITHUB_ACTION_PATH}/base-action/src/index.ts
    env:
      # ... existing env vars ...
      
      # CRITICAL: Omnara configuration from repository_dispatch
      OMNARA_API_KEY: ${{ env.OMNARA_API_KEY }}
      OMNARA_AGENT_INSTANCE_ID: ${{ env.OMNARA_AGENT_INSTANCE_ID }}
      OMNARA_AGENT_TYPE: ${{ env.OMNARA_AGENT_TYPE }}
```

## 4. Workflow Configuration

### File: `test-workflow.yml` (Example workflow for users)

**Split into two jobs for clarity:**

```yaml
name: Omnara AI Assistant

on:
  repository_dispatch:
    types: [omnara-trigger]
  issue_comment:
    types: [created]
  issues:
    types: [opened, edited]
  pull_request:
    types: [opened, edited]

jobs:
  # Job 1: Handle repository_dispatch from Omnara dashboard
  omnara-dispatch:
    runs-on: ubuntu-latest
    if: github.event_name == 'repository_dispatch'
    
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
      
      - name: Run Omnara Claude Code Action
        uses: omnara-ai/omnara/integrations/github/claude-code-action@github-action
        with:
          mode: agent  # CRITICAL: Use agent mode for automation
          direct_prompt: ${{ github.event.client_payload.prompt }}
          model: "claude-3-5-sonnet-latest"
          max_turns: 30
          timeout_minutes: 30
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
        env:
          # Pass Omnara configuration from payload
          OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key }}
          OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}
          OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type || 'Claude Code' }}
  
  # Job 2: Handle @omnara mentions
  omnara-comment:
    runs-on: ubuntu-latest
    if: |
      github.event_name != 'repository_dispatch' &&
      contains(github.event.comment.body || github.event.issue.body || github.event.pull_request.body || '', '@omnara')
    
    steps:
      - name: Checkout repository
        uses: actions/checkout@v4
      
      - name: Run Omnara Claude Code Action
        uses: omnara-ai/omnara/integrations/github/claude-code-action@github-action
        with:
          mode: tag  # Use tag mode for mentions
          trigger_phrase: "@omnara"
          model: "claude-3-5-sonnet-latest"
          max_turns: 30
          timeout_minutes: 30
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
        env:
          OMNARA_API_KEY: ${{ secrets.OMNARA_API_KEY }}
          OMNARA_AGENT_INSTANCE_ID: ${{ format('omnara-{0}', github.run_id) }}
          OMNARA_AGENT_TYPE: 'Claude Code'
```

## 5. Environment Variable Flow

The critical environment variables that need to flow through:

```
Omnara Dashboard
  ↓ (via repository_dispatch payload)
client_payload:
  - prompt
  - omnara_api_key  
  - agent_instance_id
  - agent_type
  ↓ (GitHub Actions workflow)
Environment Variables:
  - OMNARA_API_KEY
  - OMNARA_AGENT_INSTANCE_ID
  - OMNARA_AGENT_TYPE
  ↓ (action.yml)
Pass to base-action via env
  ↓ (base-action/src/run-omnara.ts)
Pass to omnara headless command
```

## 6. Key Differences from Standard Claude Code Action

1. **Execution Command**: Replace `claude-code` CLI with `omnara headless`
2. **Additional Dependencies**: Install both Claude Code and Omnara Python package
3. **Session Tracking**: Omnara provides session ID for tracking in dashboard
4. **API Key**: Requires OMNARA_API_KEY in addition to ANTHROPIC_API_KEY
5. **Prompt Handling**: Pass prompt as CLI argument, not via stdin

## Migration to v1.0

To apply these changes to Claude Code Action v1.0:

### For v1.0's Automatic Mode Detection

Since v1.0 auto-detects mode based on `prompt` presence:

```yaml
# Repository dispatch job would become:
- uses: anthropics/claude-code-action@v1
  with:
    prompt: ${{ github.event.client_payload.prompt }}  # Auto-triggers automation mode
    claude_args: |
      --max-turns 30
      --model claude-3-5-sonnet-latest
```

### Key Files to Modify in v1.0

1. **Create `base-action/src/run-omnara.ts`** - Copy the entire file from above
2. **Modify `base-action/src/index.ts`** - Import and use runOmnara instead of runClaude
3. **Update `action.yml`** - Add Omnara installation steps
4. **Ensure repository_dispatch support** in context parsing

### Environment Variable Handling in v1.0

The v1.0 settings format would be:
```yaml
settings: |
  {
    "env": {
      "OMNARA_API_KEY": "${{ github.event.client_payload.omnara_api_key }}",
      "OMNARA_AGENT_INSTANCE_ID": "${{ github.event.client_payload.agent_instance_id }}",
      "OMNARA_AGENT_TYPE": "${{ github.event.client_payload.agent_type }}"
    }
  }
```

## Testing Checklist

- [ ] Repository dispatch event triggers workflow
- [ ] Omnara environment variables are passed through
- [ ] `omnara headless` command executes successfully
- [ ] Session appears in Omnara dashboard
- [ ] Agent can interact with repository (create branches, etc.)
- [ ] Output is properly formatted and captured

## Summary

The core change is replacing direct Claude Code CLI execution with `omnara headless`, which wraps Claude Code with session management. This requires:
1. Installing both Claude Code and Omnara
2. Passing Omnara-specific environment variables
3. Using `omnara headless` command instead of `claude-code`
4. Ensuring repository_dispatch events are properly handled