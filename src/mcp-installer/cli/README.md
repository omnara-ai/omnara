# Omnara CLI

Omnara CLI automatically installs the Agent Dashboard MCP server configuration into various AI clients. This allows agents to connect to your dashboard for logging progress, asking questions, and coordinating tasks.

## 🚀 Installation

```bash
npx @omnara/cli@latest install <client> --api-key YOUR_API_KEY
```

**Supported clients:** claude-code, cursor, windsurf, cline, claude, witsy, enconvo

## 🔧 Connection Types

### **SSE (Server-Sent Events)** - *Recommended*
- **Clients:** `cursor`, `claude`
- **Benefits:** Hosted service, no setup required

### **stdio** - *Local MCP server*  
- **Clients:** `cline`, `roo-cline`, `windsurf`, `witsy`, `enconvo`
- **Benefits:** Local execution, full control

## 📦 stdio Installation Process

For stdio clients, the CLI automatically:

1. **Installs Python package:** `pip install omnara`
2. **Writes clean config:**
   ```json
   {
     "mcpServers": {
       "omnara": {
         "command": "omnara",
         "args": ["--api-key", "YOUR_API_KEY"]
       }
     }
   }
   ```
3. **Ready to use** - Client runs: `omnara --api-key YOUR_KEY`

**If auto-install fails:** Run `pip install omnara` manually.

## 🔧 Manual Setup

### SSE Configuration
```json
{
  "mcpServers": {
    "omnara": {
      "url": "https://omnara-mcp.onrender.com",
      "apiKey": "YOUR_API_KEY"
    }
  }
}
```

### stdio Configuration
```json
{
  "mcpServers": {
    "omnara": {
      "command": "omnara",
      "args": ["--api-key", "YOUR_API_KEY"]
    }
  }
}
```