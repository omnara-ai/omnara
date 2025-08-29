import * as core from "@actions/core";
import { spawn } from "child_process";
import { unlink, writeFile, stat, readFile } from "fs/promises";
import { promisify } from "util";

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

type PreparedConfig = {
  omnaraArgs: string[];
  promptPath: string;
  env: Record<string, string>;
};

function parseCustomEnvVars(claudeEnv?: string): Record<string, string> {
  if (!claudeEnv || claudeEnv.trim() === "") {
    return {};
  }

  const customEnv: Record<string, string> = {};

  // Split by lines and parse each line as KEY: VALUE
  const lines = claudeEnv.split("\n");

  for (const line of lines) {
    const trimmedLine = line.trim();
    if (trimmedLine === "" || trimmedLine.startsWith("#")) {
      continue; // Skip empty lines and comments
    }

    const colonIndex = trimmedLine.indexOf(":");
    if (colonIndex === -1) {
      continue; // Skip lines without colons
    }

    const key = trimmedLine.substring(0, colonIndex).trim();
    const value = trimmedLine.substring(colonIndex + 1).trim();

    if (key) {
      customEnv[key] = value;
    }
  }

  return customEnv;
}

export async function prepareRunConfig(
  promptPath: string,
  options: OmnaraOptions,
): Promise<PreparedConfig> {
  // Build omnara headless arguments
  const omnaraArgs = ["headless"];
  
  // Read prompt content from file to pass as --prompt argument
  let promptContent = '';
  try {
    promptContent = await readFile(promptPath, 'utf8');
    console.log(`Read prompt from file: ${promptPath} (${promptContent.length} characters)`);
  } catch (e) {
    console.error(`Error reading prompt file: ${e}`);
  }
  
  // Add required omnara arguments
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

  // Pass max turns as extra arg for Claude SDK within omnara
  if (options.maxTurns) {
    const maxTurnsNum = parseInt(options.maxTurns, 10);
    if (isNaN(maxTurnsNum) || maxTurnsNum <= 0) {
      throw new Error(
        `maxTurns must be a positive number, got: ${options.maxTurns}`,
      );
    }
    // Pass as extra args for Claude SDK
    omnaraArgs.push("--max-turns", options.maxTurns);
  }
  
  // Validate timeout
  if (options.timeoutMinutes) {
    const timeoutMinutesNum = parseInt(options.timeoutMinutes, 10);
    if (isNaN(timeoutMinutesNum) || timeoutMinutesNum <= 0) {
      throw new Error(
        `timeoutMinutes must be a positive number, got: ${options.timeoutMinutes}`,
      );
    }
  }

  // Parse custom environment variables
  const customEnv = parseCustomEnvVars(options.claudeEnv);

  if (process.env.INPUT_ACTION_INPUTS_PRESENT) {
    customEnv.GITHUB_ACTION_INPUTS = process.env.INPUT_ACTION_INPUTS_PRESENT;
  }

  return {
    omnaraArgs,
    promptPath,
    env: customEnv,
  };
}

export async function runOmnara(promptPath: string, options: OmnaraOptions) {
  const config = await prepareRunConfig(promptPath, options);

  // Log configuration for debugging
  console.log("==================================================");
  console.log("Omnara Configuration:");
  console.log(`Session ID: ${process.env.OMNARA_AGENT_INSTANCE_ID || 'auto-generated'}`);
  console.log(`Agent Type: ${process.env.OMNARA_AGENT_TYPE || 'GitHub Action'}`);
  console.log(`API Key: ${process.env.OMNARA_API_KEY ? 'Present' : 'Missing'}`);
  console.log("==================================================");
  
  // Get prompt size
  let promptSize = "unknown";
  try {
    const stats = await stat(config.promptPath);
    promptSize = stats.size.toString();
  } catch (e) {
    // Ignore error
  }

  console.log(`Prompt file size: ${promptSize} bytes`);

  // Log custom environment variables if any
  const customEnvKeys = Object.keys(config.env).filter(
    (key) => key !== "GITHUB_ACTION_INPUTS",
  );
  if (customEnvKeys.length > 0) {
    console.log(`Custom environment variables: ${customEnvKeys.join(", ")}`);
  }

  // Output to console
  console.log(`Running Omnara with command: omnara ${config.omnaraArgs.join(' ')}`);

  // Omnara headless uses Claude Code under the hood, so we need to ensure
  // Claude authentication is available in the environment
  const omnaraEnv = {
    ...process.env,
    ...config.env,
    // Ensure Omnara variables are set
    OMNARA_API_KEY: process.env.OMNARA_API_KEY,
    OMNARA_AGENT_INSTANCE_ID: process.env.OMNARA_AGENT_INSTANCE_ID,
    OMNARA_AGENT_TYPE: process.env.OMNARA_AGENT_TYPE,
    // Pass through Claude authentication (omnara will use these internally)
    ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY,
    CLAUDE_CODE_OAUTH_TOKEN: process.env.CLAUDE_CODE_OAUTH_TOKEN,
    // Pass through cloud provider credentials if using Bedrock/Vertex
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
  
  const omnaraProcess = spawn("omnara", config.omnaraArgs, {
    stdio: ["inherit", "pipe", "inherit"],
    env: omnaraEnv,
  });

  // Handle Omnara process errors
  omnaraProcess.on("error", (error) => {
    console.error("Error spawning Omnara process:", error);
    console.error("Make sure omnara is installed: pip install omnara");
  });

  // Capture output for parsing execution metrics
  let output = "";
  omnaraProcess.stdout.on("data", (data) => {
    const text = data.toString();

    // Try to parse as JSON and pretty print if it's on a single line
    const lines = text.split("\n");
    lines.forEach((line: string, index: number) => {
      if (line.trim() === "") return;

      try {
        // Check if this line is a JSON object
        const parsed = JSON.parse(line);
        const prettyJson = JSON.stringify(parsed, null, 2);
        process.stdout.write(prettyJson);
        if (index < lines.length - 1 || text.endsWith("\n")) {
          process.stdout.write("\n");
        }
      } catch (e) {
        // Not a JSON object, print as is
        process.stdout.write(line);
        if (index < lines.length - 1 || text.endsWith("\n")) {
          process.stdout.write("\n");
        }
      }
    });

    output += text;
  });

  // Handle stdout errors
  omnaraProcess.stdout.on("error", (error) => {
    console.error("Error reading Omnara stdout:", error);
  });

  // Wait for Omnara to finish with timeout
  let timeoutMs = 1440 * 60 * 1000; // Default 10 minutes
  if (options.timeoutMinutes) {
    timeoutMs = parseInt(options.timeoutMinutes, 10) * 60 * 1000;
  } else if (process.env.INPUT_TIMEOUT_MINUTES) {
    const envTimeout = parseInt(process.env.INPUT_TIMEOUT_MINUTES, 10);
    if (isNaN(envTimeout) || envTimeout <= 0) {
      throw new Error(
        `INPUT_TIMEOUT_MINUTES must be a positive number, got: ${process.env.INPUT_TIMEOUT_MINUTES}`,
      );
    }
    timeoutMs = envTimeout * 60 * 1000;
  }

  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => {
      omnaraProcess.kill("SIGTERM");
      reject(
        new Error(
          `Command timed out after ${timeoutMs / 1000 / 60} minutes`,
        ),
      );
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

  // Save output to execution file
  await writeFile(EXECUTION_FILE, output);

  // Try to extract execution metrics from output
  let executionMetrics = {
    conclusion: "success",
  };

  try {
    // Try to parse the output as JSON lines
    const lines = output.split("\n");
    for (const line of lines) {
      if (line.trim()) {
        try {
          const parsed = JSON.parse(line);
          if (parsed.metrics) {
            executionMetrics = parsed.metrics;
          }
        } catch (e) {
          // Not JSON, skip
        }
      }
    }
  } catch (error) {
    console.error("Error parsing execution metrics:", error);
  }

  // Set outputs for GitHub Actions
  core.setOutput("execution_file", EXECUTION_FILE);
  core.setOutput("conclusion", executionMetrics.conclusion);
}