import os from "node:os";
import path from "node:path";
import type { ClientConfig, ValidClient, TransportType } from "./types.js";
import { SSE_SUPPORTED_CLIENTS, HTTP_SUPPORTED_CLIENTS } from "./types.js";

const homeDir = os.homedir();

const platformPaths = {
  win32: {
    baseDir: process.env.APPDATA || path.join(homeDir, "AppData", "Roaming"),
    vscodePath: path.join("Code", "User", "globalStorage"),
  },
  darwin: {
    baseDir: path.join(homeDir, "Library", "Application Support"),
    vscodePath: path.join("Code", "User", "globalStorage"),
  },
  linux: {
    baseDir: process.env.XDG_CONFIG_HOME || path.join(homeDir, ".config"),
    vscodePath: path.join("Code/User/globalStorage"),
  },
};

const platform = process.platform as keyof typeof platformPaths;
const { baseDir, vscodePath } = platformPaths[platform];

export const clientPaths: Record<string, string> = {
  claude: path.join(baseDir, "Claude", "claude_desktop_config.json"),
  "claude-code": path.join(homeDir, ".claude.json"),
  cline: path.join(
    baseDir,
    vscodePath,
    "saoudrizwan.claude-dev",
    "settings",
    "cline_mcp_settings.json"
  ),
  "roo-cline": path.join(
    baseDir,
    vscodePath,
    "rooveterinaryinc.roo-cline",
    "settings",
    "cline_mcp_settings.json"
  ),
  windsurf: path.join(homeDir, ".codeium", "windsurf", "mcp_config.json"),
  witsy: path.join(baseDir, "Witsy", "settings.json"),
  enconvo: path.join(homeDir, ".config", "enconvo", "mcp_config.json"),
  cursor: path.join(homeDir, ".cursor", "mcp.json"),
  "github-copilot": path.join(baseDir, "Code", "User", "settings.json"),
};

const getMCPEndpoint = (customEndpoint?: string) => {
  return customEndpoint || "https://agent-dashboard-mcp.onrender.com/mcp";
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

/**
 * Create server configuration for different transport types
 */
function createServerConfig(
  transport: TransportType,
  apiKey: string,
  endpoint: string,
  client: ValidClient
): any {
  const baseHeaders = {
    Authorization: `Bearer ${apiKey}`,
    "X-Client-Type": client,
  };

  switch (transport) {
    case "sse":
      return {
        url: endpoint,
        headers: baseHeaders,
        alwaysAllow: ["log_step", "ask_question"],
        disabled: false,
        timeout: 30000,
        retry: true,
      };

    case "streamable-http":
      return {
        type: "http" as const,
        url: endpoint,
        headers: baseHeaders,
      };

    case "stdio":
    default:
      return {
        command: "pipx",
        args: ["run", "--no-cache", "omnara", "--api-key", apiKey],
        env: {
          OMNARA_CLIENT_TYPE: client
        },
      };
  }
}

/**
 * Check if client uses VS Code-style configuration structure
 */
export function usesVSCodeStyle(client: ValidClient): boolean {
  return client === "github-copilot";
}

export const getDefaultConfig = (
  client: ValidClient, 
  apiKey: string = "YOUR_API_KEY", 
  endpoint?: string,
  transportOverride?: TransportType
): ClientConfig => {
  const transport = determineTransport(client, transportOverride);
  const mcpEndpoint = getMCPEndpoint(endpoint);
  const serverConfig = createServerConfig(transport, apiKey, mcpEndpoint, client);

  // Return configuration in the appropriate format for the client
  if (usesVSCodeStyle(client)) {
    return {
      mcp: {
        servers: {
          "omnara": serverConfig,
        },
      },
    } as any; // Cast to any since this extends our base ClientConfig type
  }

  return {
    mcpServers: {
      "omnara": serverConfig,
    },
  };
};