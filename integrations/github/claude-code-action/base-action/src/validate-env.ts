import * as core from "@actions/core";

/**
 * Validates that required environment variables are present for both Claude Code and Omnara integration
 */
export function validateEnvironmentVariables() {
  const errors: string[] = [];
  
  // First check Omnara requirements
  const omnaraApiKey = process.env.OMNARA_API_KEY;
  
  if (!omnaraApiKey) {
    errors.push(
      "OMNARA_API_KEY environment variable is required. " +
      "Please ensure your workflow sets OMNARA_API_KEY from github.event.client_payload.omnara_api_key"
    );
  }
  
  // Check for agent instance ID (optional but recommended)
  if (!process.env.OMNARA_AGENT_INSTANCE_ID) {
    core.warning(
      "OMNARA_AGENT_INSTANCE_ID not set. A random session ID will be generated. " +
      "Consider setting it from github.event.client_payload.agent_instance_id for better tracking."
    );
  }

  // Check for agent type (optional)
  if (!process.env.OMNARA_AGENT_TYPE) {
    core.info(
      "OMNARA_AGENT_TYPE not set. Will default to 'GitHub Action'. " +
      "You can set it from github.event.client_payload.agent_type for custom agent names."
    );
  }
  
  // Now check Claude Code authentication requirements
  const useBedrock = process.env.CLAUDE_CODE_USE_BEDROCK === "1";
  const useVertex = process.env.CLAUDE_CODE_USE_VERTEX === "1";
  const anthropicApiKey = process.env.ANTHROPIC_API_KEY;
  const claudeCodeOAuthToken = process.env.CLAUDE_CODE_OAUTH_TOKEN;

  if (useBedrock && useVertex) {
    errors.push(
      "Cannot use both Bedrock and Vertex AI simultaneously. Please set only one provider.",
    );
  }

  if (!useBedrock && !useVertex) {
    if (!anthropicApiKey && !claudeCodeOAuthToken) {
      errors.push(
        "Either ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN is required for Claude Code. " +
        "Omnara uses Claude Code under the hood and needs Claude authentication."
      );
    }
  } else if (useBedrock) {
    const requiredBedrockVars = {
      AWS_REGION: process.env.AWS_REGION,
      AWS_ACCESS_KEY_ID: process.env.AWS_ACCESS_KEY_ID,
      AWS_SECRET_ACCESS_KEY: process.env.AWS_SECRET_ACCESS_KEY,
    };

    Object.entries(requiredBedrockVars).forEach(([key, value]) => {
      if (!value) {
        errors.push(`${key} is required when using AWS Bedrock.`);
      }
    });
  } else if (useVertex) {
    const requiredVertexVars = {
      ANTHROPIC_VERTEX_PROJECT_ID: process.env.ANTHROPIC_VERTEX_PROJECT_ID,
      CLOUD_ML_REGION: process.env.CLOUD_ML_REGION,
    };

    Object.entries(requiredVertexVars).forEach(([key, value]) => {
      if (!value) {
        errors.push(`${key} is required when using Google Vertex AI.`);
      }
    });
  }

  // If there are errors, throw them
  if (errors.length > 0) {
    const errorMessage = [
      "Environment validation failed:",
      "",
      ...errors.map((e) => `  - ${e}`),
      "",
      "Example workflow configuration:",
      "```yaml",
      "env:",
      "  # Omnara configuration (from repository_dispatch)",
      "  OMNARA_API_KEY: ${{ github.event.client_payload.omnara_api_key }}",
      "  OMNARA_AGENT_INSTANCE_ID: ${{ github.event.client_payload.agent_instance_id }}",
      "  OMNARA_AGENT_TYPE: ${{ github.event.client_payload.agent_type }}",
      "",
      "# Also need Claude authentication (one of):",
      "# Option 1: Direct API",
      "with:",
      "  anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}",
      "",
      "# Option 2: AWS Bedrock",
      "with:",
      "  use_bedrock: true",
      "# Plus AWS credentials...",
      "```",
    ].join("\n");

    throw new Error(errorMessage);
  }

  // Log successful validation
  core.info("✓ Environment variables validated successfully");
  core.info("Omnara Configuration:");
  core.info(`  - API Key: Present`);
  core.info(`  - Session ID: ${process.env.OMNARA_AGENT_INSTANCE_ID || "Will be auto-generated"}`);
  core.info(`  - Agent Type: ${process.env.OMNARA_AGENT_TYPE || "GitHub Action (default)"}`);
  
  core.info("Claude Authentication:");
  if (useBedrock) {
    core.info("  - Using AWS Bedrock");
  } else if (useVertex) {
    core.info("  - Using Google Vertex AI");
  } else if (anthropicApiKey) {
    core.info("  - Using Anthropic API key");
  } else if (claudeCodeOAuthToken) {
    core.info("  - Using Claude Code OAuth token");
  }
}