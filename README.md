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

Omnara uses the **Model Context Protocol (MCP)** to enable seamless communication between your agents and the dashboard.

```mermaid
graph TB
    subgraph "Your AI Agents"
        A[🤖 AI Agents<br/>Claude, Cursor, Copilot]
    end

    subgraph "Omnara Platform"
        S[🔄 MCP Server]
        DB[(📊 Database)]
        API[🌐 API Server]
    end

    subgraph "Your Devices"
        M[📱 Mobile App]
        W[💻 Web Dashboard]
    end

    A -->|Log activities| S
    S -->|Store data| DB
    DB -->|Real-time sync| API
    API -->|Push updates| M
    API -->|Push updates| W
    M -->|Send feedback| API
    W -->|Send feedback| API
    API -->|Store feedback| DB
    S <-->|Agent queries| DB

    style A fill:#e3f2fd,stroke:#1976d2,stroke-width:3px
    style S fill:#c8e6c9,stroke:#388e3c,stroke-width:2px
    style DB fill:#ffccbc,stroke:#d84315,stroke-width:2px
    style API fill:#c8e6c9,stroke:#388e3c,stroke-width:2px
    style M fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
    style W fill:#f8bbd0,stroke:#c2185b,stroke-width:3px
```

### 🔧 Technical Stack

- **Backend**: FastAPI with separate read/write servers for optimal performance
- **Frontend**: React (Web) + React Native (Mobile)
- **Protocol**: Model Context Protocol (MCP) + REST API
- **Database**: PostgreSQL with SQLAlchemy ORM
- **Auth**: Dual JWT system (Supabase for users, custom for agents)

## 🚀 Quick Start

### For Claude Code Users

1. **Download the app** or visit [omnara.ai](https://omnara.ai)
2. **Launch the webhook server** with the command in the onboarding flow
3. **Create your agent** with the webhook endpoint and API key
4. **Start monitoring** your AI workforce!

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

### Method 1: MCP Configuration
```json
{
  "mcpServers": {
    "omnara": {
      "url": "https://api.omnara.ai/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_API_KEY"
      }
    }
  }
}
```

### Method 2: Python SDK
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

### Method 3: REST API
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
| **Free** | $0/mo | 20 agents/month, Core features |
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