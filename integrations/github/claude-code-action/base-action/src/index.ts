#!/usr/bin/env bun

import * as core from "@actions/core";
import { preparePrompt } from "./prepare-prompt";
import { runClaude } from "./run-claude";
import { runOmnara } from "./run-omnara";
import { setupClaudeCodeSettings } from "./setup-claude-code-settings";
import { validateEnvironmentVariables } from "./validate-env";

async function run() {
  try {
    validateEnvironmentVariables();

    await setupClaudeCodeSettings(
      process.env.INPUT_SETTINGS,
      undefined, // homeDir
    );

    const promptConfig = await preparePrompt({
      prompt: process.env.INPUT_PROMPT || "",
      promptFile: process.env.INPUT_PROMPT_FILE || "",
    });

    // Dynamically determine whether to use Omnara based on:
    // 1. OMNARA_API_KEY is present (from repository_dispatch or secrets)
    // 2. Trigger phrase contains "omnara" (case-insensitive)
    // This allows both @claude (regular) and @omnara (with tracking) in same repo
    const triggerPhrase = process.env.TRIGGER_PHRASE || "@claude";
    const isOmnaraTrigger = triggerPhrase.toLowerCase().includes("omnara");
    const hasOmnaraKey = !!process.env.OMNARA_API_KEY;
    const isRepositoryDispatch = process.env.GITHUB_EVENT_NAME === "repository_dispatch";
    
    // Check if @omnara was used without API key - fall back to Claude with warning
    if (isOmnaraTrigger && !hasOmnaraKey) {
      console.warn("⚠️ WARNING: @omnara was used but OMNARA_API_KEY is not set in repository secrets.");
      console.warn("⚠️ Falling back to standard Claude Code without session tracking.");
      console.warn("⚠️ To enable Omnara tracking, add OMNARA_API_KEY to your repository secrets.");
    }
    
    if (hasOmnaraKey && (isOmnaraTrigger || isRepositoryDispatch)) {
      // Use Omnara headless for session tracking
      console.log("✅ Using Omnara headless for session tracking");
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
        timeoutMinutes: process.env.INPUT_TIMEOUT_MINUTES,
      });
    } else {
      // Use standard Claude Code
      console.log("ℹ️ Using standard Claude Code (no Omnara tracking)");
      await runClaude(promptConfig.path, {
        claudeArgs: process.env.INPUT_CLAUDE_ARGS,
        allowedTools: process.env.INPUT_ALLOWED_TOOLS,
        disallowedTools: process.env.INPUT_DISALLOWED_TOOLS,
        maxTurns: process.env.INPUT_MAX_TURNS,
        mcpConfig: process.env.INPUT_MCP_CONFIG,
        systemPrompt: process.env.INPUT_SYSTEM_PROMPT,
        appendSystemPrompt: process.env.INPUT_APPEND_SYSTEM_PROMPT,
        claudeEnv: process.env.INPUT_CLAUDE_ENV,
        fallbackModel: process.env.INPUT_FALLBACK_MODEL,
        model: process.env.ANTHROPIC_MODEL,
        pathToClaudeCodeExecutable:
          process.env.INPUT_PATH_TO_CLAUDE_CODE_EXECUTABLE,
      });
    }
  } catch (error) {
    core.setFailed(`Action failed with error: ${error}`);
    core.setOutput("conclusion", "failure");
    process.exit(1);
  }
}

if (import.meta.main) {
  run();
}
