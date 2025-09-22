import fs from "node:fs";
import path from "node:path";
import * as jsonc from "jsonc-parser";
import type { ValidClient, ClientConfig } from "./types.js";
import { clientPaths, usesVSCodeStyle } from "./config.js";

export function getConfigPath(client: ValidClient): string {
  const configPath = clientPaths[client];
  if (!configPath) {
    throw new Error(`Invalid client: ${client}`);
  }
  return configPath;
}

/**
 * Parse JSON with support for comments (like VS Code settings.json)
 */
function parseJsonWithComments(jsonString: string): any {
  try {
    // First try standard JSON parsing
    return JSON.parse(jsonString);
  } catch {
    // Use proper JSONC parser for VS Code style files
    const errors: jsonc.ParseError[] = [];
    const result = jsonc.parse(jsonString, errors);
    
    if (errors.length > 0) {
      console.warn(`JSONC parsing warnings:`, errors);
    }
    
    return result;
  }
}

/**
 * Safely read and parse a config file
 */
function readConfigFile(configPath: string): any {
  if (!fs.existsSync(configPath)) {
    return {};
  }

  try {
    const fileContent = fs.readFileSync(configPath, "utf8");
    return parseJsonWithComments(fileContent);
  } catch (error) {
    console.warn(`Warning: Could not read config file ${configPath}:`, error);
    return {};
  }
}


/**
 * Merge configurations based on client type
 */
function mergeConfigs(existingConfig: any, newConfig: any, client: ValidClient): any {
  if (usesVSCodeStyle(client)) {
    // VS Code style: mcp.servers
    const merged = { ...existingConfig };
    
    if (!merged.mcp) merged.mcp = {};
    if (!merged.mcp.servers) merged.mcp.servers = {};
    
    if (newConfig.mcp?.servers) {
      merged.mcp.servers = {
        ...merged.mcp.servers,
        ...newConfig.mcp.servers,
      };
    }
    
    return merged;
  } else {
    // Standard style: mcpServers
    return {
      ...existingConfig,
      mcpServers: {
        ...existingConfig.mcpServers,
        ...newConfig.mcpServers,
      },
    };
  }
}

export function readConfig(client: ValidClient): ClientConfig {
  const configPath = getConfigPath(client);
  const rawConfig = readConfigFile(configPath);
  
  if (usesVSCodeStyle(client)) {
    // Convert VS Code style to standard for consistency
    return {
      mcpServers: rawConfig.mcp?.servers || {},
    };
  }
  
  return {
    ...rawConfig,
    mcpServers: rawConfig.mcpServers || {},
  };
}

export function writeConfig(client: ValidClient, config: ClientConfig): void {
  const configPath = getConfigPath(client);
  const configDir = path.dirname(configPath);

  if (!fs.existsSync(configDir)) {
    fs.mkdirSync(configDir, { recursive: true });
  }

  // Validate config structure
  if (!config.mcpServers && !(config as any).mcp?.servers) {
    throw new Error("Invalid config structure");
  }

  // Read existing config
  const existingConfig = readConfigFile(configPath);
  
  // Merge configurations
  const mergedConfig = mergeConfigs(existingConfig, config, client);
  
  // Write back with appropriate formatting
  const indent = usesVSCodeStyle(client) ? 4 : 2;
  fs.writeFileSync(configPath, JSON.stringify(mergedConfig, null, indent));
}