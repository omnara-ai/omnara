# Omnara - Mission Control for Your AI Agents 🚀

**Your AI workforce launchpad, in your pocket.**

![Omnara Mobile Experience](./docs/assets/three-panel.png)

<div align="center">

[📱 **Download iOS App**](https://apps.apple.com/us/app/omnara-ai-command-center/id6748426727) • [🌐 **Try Web Dashboard**](https://omnara.ai) • [⭐ **Star on GitHub**](https://github.com/omnara-ai/omnara)

</div>

---

## 🚀 What is Omnara?

Omnara transforms your AI agents (Claude Code, Cursor, GitHub Copilot, and more) from silent workers into communicative teammates. Get real-time visibility into what your agents are doing, respond to their questions instantly, and guide them to success - all from your phone.

### ✨ Key Features

| Feature | Description |
|---------|------------|
| **📊 Real-Time Monitoring** | See every step your AI agents take as they work |
| **💬 Interactive Q&A** | Respond instantly when agents need guidance |
| **📱 Mobile-First Design** | Full control from your phone, tablet, or desktop |
| **🔔 Smart Notifications** | Get alerted only when your input is needed |
| **🎯 Universal Dashboard** | All your AI agents in one unified interface |

### 🎬 See It In Action

![Mobile Notifications](./docs/assets/iNotifications-Stack.gif)

> *The moment your agent needs help, you're there. No more returning to failed jobs hours later.*

![Agent Activity Feed](./docs/assets/Mobile-app-showcase.gif)

## 💡 Why Omnara?

We built Omnara because we were tired of:
- ❌ Starting long agent jobs and finding them stuck hours later
- ❌ Missing critical questions that blocked progress
- ❌ Having no visibility into what our AI was actually doing
- ❌ Being tied to our desks while agents worked

**Now you can:**
- ✅ Launch agents and monitor them from anywhere
- ✅ Get push notifications when input is needed
- ✅ Send real-time feedback to guide your agents
- ✅ Have confidence your AI workforce is productive

## 🏗️ Architecture Overview

Omnara provides a unified platform for monitoring and controlling your AI agents:

```mermaid
graph TB
    subgraph "Your AI Agents"
        A[🤖 AI Agents<br/>Claude Code, Cursor, etc.]
    end

    subgraph "Omnara Platform"
        API[🌐 API Server]
        DB[(📊 PostgreSQL)]
        NOTIFY[🔔 Notification Service<br/>Push/Email/SMS]
    end

    subgraph "Your Devices"
        M[📱 Mobile App]
        W[💻 Web Dashboard]
    end

    A -->|Send updates| API
    API -->|Store data| DB
    API -->|Trigger notifications| NOTIFY
    NOTIFY -->|Alert users| M
    DB -->|Real-time sync| M
    DB -->|Real-time sync| W
    M -->|User responses| API
    W -->|User responses| API
    API -->|Deliver feedback| A

    style A fill:#e3f2fd,stroke:#1976d2,stroke-width:3px
    style API fill:#c8e6c9,stroke:#388e3c,stroke-width:2px
    style DB fill:#ffccbc,stroke:#d84315,stroke-width:2px
    style NOTIFY fill:#fff59d,stroke:#f57f17,stroke-width:2px
    style M fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
    style W fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
```

Omnara supports two distinct modes of operation:

### Mode 1: Real-Time Claude Code Monitoring

When you run Claude Code locally with the Omnara wrapper, it monitors your agent's activity in real-time:

```mermaid
graph TB
    subgraph "Your Local Machine"
        CC[🤖 Claude Code]
        CW[📊 Claude Wrapper<br/>claude_wrapper_v3.py]
        LOG[📄 JSONL Logs<br/>~/.claude/projects/]
        TERM[🖥️ Terminal Output]
    end

    subgraph "Omnara Platform"
        API[🌐 REST API Server]
        DB[(📊 PostgreSQL)]
        SSE[📡 SSE Endpoint]
        NOTIFY[🔔 Notification Service<br/>Push/Email/SMS]
    end

    subgraph "Your Devices"
        M[📱 Mobile App]
        W[💻 Web Dashboard]
    end

    CC -->|Writes logs| LOG
    CC -->|Displays output| TERM
    CW -->|Monitors| LOG
    CW -->|Watches| TERM
    CW -->|Sends messages via REST| API
    API -->|Stores messages| DB
    API -->|Triggers notifications| NOTIFY
    DB -->|NOTIFY triggers| SSE
    SSE -->|Real-time updates| M
    SSE -->|Real-time updates| W
    NOTIFY -->|Push/Email/SMS| M
    M -->|User responses| API
    W -->|User responses| API
    API -->|Queued messages| CW
    CW -->|Types into| CC

    style CC fill:#e3f2fd,stroke:#1976d2,stroke-width:3px
    style CW fill:#fff59d,stroke:#f57f17,stroke-width:2px
    style API fill:#c8e6c9,stroke:#388e3c,stroke-width:2px
    style DB fill:#ffccbc,stroke:#d84315,stroke-width:2px
    style NOTIFY fill:#ffe082,stroke:#f57f17,stroke-width:2px
    style M fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
    style W fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
```

**How it works:**
- The wrapper monitors Claude's JSONL log files and terminal output
- It identifies questions by detecting idle states and parsing terminal prompts
- Messages are sent to Omnara via REST API with `requires_user_input` flag
- PostgreSQL triggers send real-time notifications via SSE
- Users receive push/email/SMS notifications for questions
- Responses are injected back into Claude's terminal

### Mode 2: Remote Agent Launch via Webhook

When you trigger an agent remotely from Omnara, it uses the Model Context Protocol (MCP):

```mermaid
graph TB
    subgraph "Omnara Platform"
        UI[🌐 Web/Mobile UI]
        API[🌐 API Server<br/>+ MCP Endpoint]
        DB[(📊 PostgreSQL)]
        SSE[📡 SSE Endpoint]
    end

    subgraph "Your Computer"
        WH[🔗 Webhook Server<br/>claude_code.py]
        CT[☁️ Cloudflare Tunnel<br/>optional]
        CC[🤖 Claude Code<br/>w/ MCP Config]
    end

    UI -->|Trigger agent| API
    API -->|POST webhook| CT
    CT -->|Forward request| WH
    WH -->|Launch Claude| CC
    CC -->|log_step/ask_question<br/>via MCP| API
    API -->|Store messages| DB
    DB -->|Real-time updates| SSE
    SSE -->|Push updates| UI
    UI -->|User responses| API
    API -->|Return answers<br/>via MCP| CC

    style UI fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
    style WH fill:#fff59d,stroke:#f57f17,stroke-width:2px
    style CC fill:#e3f2fd,stroke:#1976d2,stroke-width:3px
    style API fill:#c8e6c9,stroke:#388e3c,stroke-width:2px
    style DB fill:#ffccbc,stroke:#d84315,stroke-width:2px
    style SSE fill:#dcedc8,stroke:#689f38,stroke-width:2px
```

**How it works:**
- User triggers an agent from the Omnara interface
- Webhook server receives the request (optionally via Cloudflare tunnel)
- Claude is launched with MCP configuration pointing to Omnara
- The system prompt enforces Omnara-only communication (no terminal output)
- All communication flows through `log_step` and `ask_question` MCP tools
- Questions are explicitly marked with `requires_user_input=true`

### 🔄 Mode Comparison

| Feature | Real-Time Monitoring | Remote Launch |
|---------|---------------------|---------------|
| **Best for** | Local development | Remote automation |
| **Setup complexity** | Simple (just run wrapper) | Moderate (webhook + tunnel) |
| **Claude output visible** | Yes (in terminal) | No (MCP only) |
| **Question detection** | Automatic (terminal parsing) | Explicit (ask_question) |
| **Git isolation** | Uses current directory | Creates worktrees |
| **Parallel sessions** | Not recommended | Fully supported |
| **Network requirements** | API access only | Webhook endpoint |

### 🔧 Technical Stack

- **Backend**: FastAPI with separate read/write servers for optimal performance
- **Frontend**: React (Web) + React Native (Mobile)
- **Protocol**: Model Context Protocol (MCP) + REST API
- **Database**: PostgreSQL with SQLAlchemy ORM
- **Auth**: Dual JWT system (Supabase for users, custom for agents)

## 🚀 Quick Start

### Option 1: Real-Time Monitoring (Recommended for local development)

Monitor your Claude Code sessions in real-time:

1. **Download the app** or visit [omnara.ai](https://omnara.ai)
2. **Get your API key** from the dashboard
3. **Run Claude with the wrapper**:
   ```bash
   python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY
   ```
4. **See everything** your agent does in the Omnara dashboard!

### Option 2: Remote Agent Launch (For triggering agents from anywhere)

Launch Claude Code remotely from your phone or web browser:

1. **Download the app** or visit [omnara.ai](https://omnara.ai)
2. **Install Omnara CLI**:
   ```bash
   pip install omnara
   # or with uv: uv pip install omnara
   ```
3. **Start the webhook server** on your computer:
   ```bash
   omnara serve
   ```
4. **Create your agent** with the webhook URL and API key shown
5. **Trigger agents remotely** from anywhere!

### For Developers

<details>
<summary><b>🛠️ Development Setup</b></summary>

#### Prerequisites
- Python 3.10+
- PostgreSQL
- Node.js (for CLI tools)

#### Setup Steps

1. **Clone and enter the repository**
   ```bash
   git clone https://github.com/omnara-ai/omnara
   cd omnara
   ```

2. **Set up Python environment**
   ```bash
   python -m venv .venv
   source .venv/bin/activate  # Windows: .venv\Scripts\activate
   make dev-install
   ```

3. **Generate JWT keys**
   ```bash
   python scripts/generate_jwt_keys.py
   ```

4. **Configure environment** (create `.env` file)
   ```env
   DATABASE_URL=postgresql://user:password@localhost:5432/omnara
   SUPABASE_URL=https://your-project.supabase.co
   SUPABASE_ANON_KEY=your-anon-key
   JWT_PRIVATE_KEY='-----BEGIN RSA PRIVATE KEY-----\n...'
   JWT_PUBLIC_KEY='-----BEGIN PUBLIC KEY-----\n...'
   ```

5. **Initialize database**
   ```bash
   cd shared/
   alembic upgrade head
   cd ..
   ```

6. **Run services**
   ```bash
   # Terminal 1: MCP + REST Server
   python -m servers.app
   
   # Terminal 2: Backend API
   cd backend && python -m main
   ```

</details>

## 📚 Integration Guide

### Method 1: Real-Time Monitoring with Wrapper

Run Claude Code with the Omnara wrapper for automatic monitoring:

```bash
# Basic usage
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY

# With git diff tracking
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY --git-diff

# Custom API endpoint (for self-hosted)
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY --base-url https://your-server.com
```

### Method 2: Remote Launch with MCP

For remote agent launching, the webhook automatically configures MCP:

```json
{
  "mcpServers": {
    "omnara": {
      "command": "pipx",
      "args": ["run", "--no-cache", "omnara", "mcp", "--api-key", "YOUR_API_KEY"]
    }
  }
}
```

### Method 3: Python SDK
```python
from omnara import OmnaraClient
import uuid

client = OmnaraClient(api_key="your-api-key")
instance_id = str(uuid.uuid4())

# Log progress and check for user feedback
response = client.send_message(
    agent_type="claude-code",
    content="Analyzing codebase structure",
    agent_instance_id=instance_id,
    requires_user_input=False
)

# Ask for user input when needed
answer = client.send_message(
    content="Should I refactor this legacy module?",
    agent_instance_id=instance_id,
    requires_user_input=True
)
```

### Method 4: REST API
```bash
curl -X POST https://api.omnara.ai/api/v1/messages/agent \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"content": "Starting deployment process", "agent_type": "claude-code", "requires_user_input": false}'
```

## 🤝 Contributing

We love contributions! Check out our [Contributing Guide](CONTRIBUTING.md) to get started.

### Development Commands
```bash
make lint       # Run code quality checks
make format     # Auto-format code
make test       # Run test suite
make dev-serve  # Start development servers
```

## 📊 Pricing

| Plan | Price | Features |
|------|-------|----------|
| **Free** | $0/mo | 10 agents/month, Core features |
| **Pro** | $9/mo | Unlimited agents, Priority support |
| **Enterprise** | [Contact Us](https://cal.com/ishaan-sehgal-8kc22w/omnara-demo) | Teams, SSO, Custom integrations |

## 🆘 Support

- 📖 [Documentation](https://docs.omnara.ai)
- 💬 [GitHub Discussions](https://github.com/omnara-ai/omnara/discussions)
- 🐛 [Report Issues](https://github.com/omnara-ai/omnara/issues)
- 📧 [Email Support](mailto:ishaan@omnara.com)

## 📜 License

Omnara is open source software licensed under the [Apache 2.0 License](LICENSE).

---

<div align="center">

**Built with ❤️ by the Omnara team**

[Website](https://omnara.ai) • [Twitter](https://twitter.com/omnara_ai) • [LinkedIn](https://linkedin.com/company/omnara)

</div>