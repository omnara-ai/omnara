export type ValidClient =
  | "claude"
  | "claude-code"
  | "cline"
  | "roo-cline"
  | "windsurf"
  | "witsy"
  | "enconvo"
  | "cursor"
  | "github-copilot";

export const VALID_CLIENTS: ValidClient[] = [
  "claude",
  "claude-code",
  "cline",
  "roo-cline",
  "windsurf",
  "witsy",
  "enconvo",
  "cursor",
  "github-copilot",
];

export type TransportType = "stdio" | "sse" | "streamable-http";

// Clients that support SSE instead of stdio
export const SSE_SUPPORTED_CLIENTS: ValidClient[] = [
  "claude-code",
  "cline",
  "roo-cline",
  "windsurf",
  "enconvo",
];

// Clients that support HTTP streamable
export const HTTP_SUPPORTED_CLIENTS: ValidClient[] = [
  "claude-code",
  "cursor",
  "witsy",
  "github-copilot",
];

export interface ServerConfig {
  command: string;
  args: string[];
  env?: Record<string, string>;
}

export interface SSEServerConfig {
  url: string;
  headers: {
    Authorization: string;
    [key: string]: string;
  };
  alwaysAllow?: string[];
  disabled?: boolean;
  timeout?: number;
  retry?: boolean;
}

export interface HTTPServerConfig {
  type: "http";
  url: string;
  headers: {
    Authorization: string;
    [key: string]: string;
  };
}

export interface ClientConfig {
  mcpServers: Record<string, ServerConfig | SSEServerConfig | HTTPServerConfig>;
}

export interface InstallOptions {
  apiKey?: string;
  transport?: TransportType;
  endpoint?: string;
}