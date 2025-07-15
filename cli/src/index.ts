#!/usr/bin/env node

import type { ValidClient, InstallOptions, TransportType } from "./types.js";
import { SSE_SUPPORTED_CLIENTS, HTTP_SUPPORTED_CLIENTS, VALID_CLIENTS } from "./types.js";
import { getDefaultConfig } from "./config.js";
import { writeConfig } from "./utils.js";
import { promptForRestart } from "./client.js";
import ora from "ora";
import chalk from "chalk";
import { exec } from "node:child_process";
import { promisify } from "node:util";
import inquirer from "inquirer";

const execAsync = promisify(exec);

const getTransportName = (transport: TransportType): string => {
  switch (transport) {
    case "sse":
      return "SSE";
    case "streamable-http":
      return "HTTP streamable";
    case "stdio":
    default:
      return "stdio (pipx)";
  }
};

const determineTransport = (client: ValidClient, transportOverride?: TransportType): TransportType => {
  if (transportOverride) {
    return transportOverride;
  }

  if (HTTP_SUPPORTED_CLIENTS.includes(client)) {
    return "streamable-http";
  }
  
  if (SSE_SUPPORTED_CLIENTS.includes(client)) {
    return "sse";
  }
  
  return "stdio";
};

async function isPipxInstalled(): Promise<boolean> {
  try {
    await execAsync("pipx --version");
    return true;
  } catch {
    return false;
  }
}

async function installPipx(): Promise<boolean> {
  const spinner = ora("Installing pipx...").start();
  
  try {
    // Check Python availability
    try {
      await execAsync("python3 --version");
    } catch {
      spinner.fail("Python 3 is not installed. Please install Python 3 first.");
      return false;
    }
    
    // Install pipx
    await execAsync("python3 -m pip install --user pipx");
    
    // Ensure PATH on Unix systems
    if (process.platform !== "win32") {
      try {
        await execAsync("python3 -m pipx ensurepath");
        spinner.succeed("Pipx installed successfully");
        console.log(chalk.yellow("\n⚠️  Restart your terminal or run: source ~/.bashrc"));
      } catch {
        // PATH setup failed but pipx is installed
        spinner.succeed("Pipx installed (manual PATH setup may be needed)");
      }
    } else {
      spinner.succeed("Pipx installed successfully");
    }
    
    return true;
  } catch (error) {
    spinner.fail("Failed to install pipx");
    console.log(chalk.yellow("\nInstall manually: pip install pipx"));
    return false;
  }
}

async function ensurePipxInstalled(): Promise<{ success: boolean; fallbackTransport?: TransportType }> {
  if (await isPipxInstalled()) {
    return { success: true };
  }
  
  console.log(chalk.yellow("\n⚠️  Pipx is required for stdio transport"));
  
  const { shouldInstall } = await inquirer.prompt<{ shouldInstall: boolean }>([
    {
      type: "confirm",
      name: "shouldInstall",
      message: "Install pipx now?",
      default: true,
    },
  ]);
  
  if (shouldInstall) {
    const installed = await installPipx();
    if (installed) {
      return { success: true };
    }
  }
  
  // Offer streamable-http as fallback when pipx installation fails
  console.log(chalk.yellow("\n💡 Pipx installation failed or was declined"));
  const { useFallback } = await inquirer.prompt<{ useFallback: boolean }>([
    {
      type: "confirm",
      name: "useFallback",
      message: "Use streamable-http transport instead? (connects to remote server)",
      default: true,
    },
  ]);
  
  if (useFallback) {
    return { success: true, fallbackTransport: "streamable-http" };
  }
  
  console.log(chalk.yellow("Install manually: pip install pipx"));
  return { success: false };
}

export async function install(
  client: ValidClient,
  options?: InstallOptions
): Promise<void> {
  const capitalizedClient = client.charAt(0).toUpperCase() + client.slice(1);
  const transport = determineTransport(client, options?.transport);
  const transportName = getTransportName(transport);

  const spinner = ora(
    `Installing ${transportName} configuration for ${capitalizedClient}...`
  ).start();

  try {
    const config = getDefaultConfig(client, options?.apiKey, options?.endpoint, options?.transport);

    writeConfig(client, config);
    spinner.succeed(
      `Successfully installed ${transportName} configuration for ${capitalizedClient}`
    );

    if (!options?.apiKey) {
      console.log(
        chalk.yellow(
          "No API key provided. Using default 'YOUR_API_KEY' placeholder."
        )
      );
    }

    // Provide specific guidance based on transport type
    switch (transport) {
      case "sse":
        console.log(
          chalk.blue(`${capitalizedClient} will connect to Omnara via SSE endpoint`)
        );
        break;
      case "streamable-http":
        console.log(
          chalk.blue(`${capitalizedClient} will connect to Omnara via HTTP streamable endpoint`)
        );
        break;
      case "stdio":
        console.log(
          chalk.blue(`${capitalizedClient} will run Omnara locally using pipx`)
        );
        
        const pipxResult = await ensurePipxInstalled();
        if (pipxResult.success) {
          if (pipxResult.fallbackTransport) {
            // User chose fallback transport, regenerate config with new transport
            console.log(chalk.yellow(`📡 Switching to ${pipxResult.fallbackTransport} transport`));
            const fallbackConfig = getDefaultConfig(client, options?.apiKey, options?.endpoint, pipxResult.fallbackTransport);
            writeConfig(client, fallbackConfig);
            console.log(chalk.blue(`${capitalizedClient} will connect to Omnara via ${getTransportName(pipxResult.fallbackTransport)} endpoint`));
          } else {
            console.log(chalk.green("✓ Pipx ready"));
          }
        } else {
          console.log(chalk.red("⚠️  Stdio transport requires pipx"));
        }
        break;
    }

    console.log(
      chalk.green(`${capitalizedClient} configuration updated successfully`)
    );
    console.log(
      chalk.yellow(
        `You may need to restart ${capitalizedClient} to see the Omnara MCP server.`
      )
    );
    await promptForRestart(client);
  } catch (error) {
    spinner.fail(`Failed to install configuration for ${capitalizedClient}`);
    console.error(
      chalk.red(
        `Error: ${error instanceof Error ? error.message : String(error)}`
      )
    );
    throw error;
  }
}

export async function installAll(options?: InstallOptions): Promise<void> {
  console.log(chalk.blue("🚀 Setting up Omnara MCP for all supported AI clients..."));
  console.log(chalk.gray("ℹ️  This safely adds configuration files - only clients you actually use will be affected."));
  console.log(chalk.gray("ℹ️  No AI clients will be installed or modified, just their MCP settings."));
  
  const results: { client: ValidClient; success: boolean; error?: string }[] = [];
  
  for (const client of VALID_CLIENTS) {
    try {
      console.log(chalk.blue(`\n📝 Configuring ${client}...`));
      await install(client, options);
      results.push({ client, success: true });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      console.error(chalk.red(`❌ Failed to configure ${client}: ${errorMessage}`));
      results.push({ client, success: false, error: errorMessage });
    }
  }
  
  // Summary
  console.log(chalk.blue("\n" + "=".repeat(50)));
  console.log(chalk.blue("📊 Configuration Summary"));
  console.log(chalk.blue("=".repeat(50)));
  
  const successful = results.filter(r => r.success);
  const failed = results.filter(r => !r.success);
  
  if (successful.length > 0) {
    console.log(chalk.green(`✅ Successfully configured ${successful.length} clients:`));
    successful.forEach(({ client }) => {
      console.log(chalk.green(`   • ${client}`));
    });
  }
  
  if (failed.length > 0) {
    console.log(chalk.red(`\n❌ Failed to configure ${failed.length} clients:`));
    failed.forEach(({ client, error }) => {
      console.log(chalk.red(`   • ${client}: ${error}`));
    });
  }
  
  console.log(chalk.blue(`\n🎉 Setup complete! ${successful.length}/${VALID_CLIENTS.length} clients configured.`));
  console.log(chalk.gray("💡 Only AI clients you actually have installed will use these configurations."));
}