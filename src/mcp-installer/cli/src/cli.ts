#!/usr/bin/env node

import { Command } from "commander";
import { install, installAll } from "./index.js";
import { VALID_CLIENTS } from "./types.js";
import chalk from "chalk";

const program = new Command();

program
  .name("omnara-cli")
  .description("Install MCP configuration for various AI clients")
  .version("1.0.0");

program
  .command("install")
  .description("Install MCP configuration for a specific client, or all clients if none specified")
  .argument(
    "[client]",
    `The client to install for (${VALID_CLIENTS.join(", ")}) - omit to install for all clients`
  )
  .option("--api-key <key>", "API key for omnara services")
  .option("--transport <transport>", "Override default transport method (stdio, sse, streamable-http)")
  .option("--endpoint <url>", "Custom endpoint URL (for SSE/HTTP/Streamable clients)")
  .action(async (client?: string, options: { apiKey?: string; transport?: string; endpoint?: string } = {}) => {
    // If no client specified, install for all clients
    if (!client) {
      // console.log(chalk.blue("No specific client provided."));
      console.log(chalk.gray("📝 This will add MCP configurations for all supported AI clients."));
      console.log(chalk.gray("💡 Only clients you actually have installed will use these configs."));
      console.log(chalk.gray(`📋 Supported clients: ${VALID_CLIENTS.join(", ")}`));
      
      try {
        await installAll({ 
          apiKey: options.apiKey, 
          transport: options.transport as any,
          endpoint: options.endpoint 
        });
      } catch (error) {
        console.error(
          chalk.red(
            error instanceof Error ? error.message : "Unknown error occurred"
          )
        );
        process.exit(1);
      }
      return;
    }

    // Validate specific client
    if (!VALID_CLIENTS.includes(client as any)) {
      console.error(
        chalk.red(
          `Invalid client "${client}". Available clients: ${VALID_CLIENTS.join(
            ", "
          )}`
        )
      );
      process.exit(1);
    }

    // Install for specific client
    try {
      await install(client as any, { 
        apiKey: options.apiKey, 
        transport: options.transport as any,
        endpoint: options.endpoint 
      });
    } catch (error) {
      console.error(
        chalk.red(
          error instanceof Error ? error.message : "Unknown error occurred"
        )
      );
      process.exit(1);
    }
  });

program.parse();