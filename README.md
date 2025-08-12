# Omnara - Mission Control for Your AI Agents 🚀

**Your AI workforce launchpad, in your pocket.**

<div align="center">

[![PyPI version](https://badge.fury.io/py/omnara.svg)](https://badge.fury.io/py/omnara)
[![Downloads](https://pepy.tech/badge/omnara)](https://pepy.tech/project/omnara)
[![Python Versions](https://img.shields.io/pypi/pyversions/omnara.svg)](https://pypi.org/project/omnara/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![GitHub stars](https://img.shields.io/github/stars/omnara-ai/omnara?style=social)](https://github.com/omnara-ai/omnara)
[![Ruff](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/astral-sh/ruff/main/assets/badge/v2.json)](https://github.com/astral-sh/ruff)

</div>

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

## 🎯 Real-World Use Cases

### 🔍 **Code Review Assistant**
Launch Claude to review PRs while you're at lunch. Get notified only if it needs clarification on architectural decisions.

### 🚨 **Production Firefighter**
Debug production issues from your phone at 2am. See exactly what your agent is investigating and guide it to the right logs.

### 📊 **Data Pipeline Guardian**
Start a 6-hour data migration before leaving work. Get alerts if anything looks suspicious, approve schema changes on the go.

### 🏗️ **Refactoring Copilot**
Let Claude refactor that legacy module while you're in meetings. Answer its questions about business logic without context switching.

### 🧪 **Test Suite Doctor**
Have Claude fix failing tests overnight. Wake up to either green builds or specific questions about expected behavior.

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

### 🚀 How It Works

**1. Connect Your Agent** → Install Omnara SDK or wrapper  
**2. Get Real-Time Updates** → See every step your agent takes  
**3. Respond Instantly** → Answer questions from anywhere  

### 🔄 Two Ways to Use Omnara

| Mode | Setup | How It Works |
|------|-------|-------------|
| **Real-Time Monitoring** | `omnara` or `uv run omnara` | Monitor your Claude session, forwards to Omnara |
| **Remote Launch** | `omnara serve` or `uv run omnara serve` | Launch agents from phone, communicate via MCP |

### 🔧 Technical Stack

- **Backend**: FastAPI with separate read/write servers for optimal performance
- **Frontend**: React (Web) + React Native (Mobile)
- **Protocol**: Model Context Protocol (MCP) + REST API
- **Database**: PostgreSQL with SQLAlchemy ORM
- **Auth**: Dual JWT system (Supabase for users, custom for agents)

## 🚀 Quick Start

### Option 1: Monitor Your Claude Sessions

See what Claude is doing in real-time:

1. **Install Omnara**:
   ```bash
   # Using pip
   pip install omnara
   
   # Using uv (faster)
   uv pip install omnara
   ```
2. **Start monitoring**:
   ```bash
   # If installed with pip
   omnara
   
   # If installed with uv
   uv run omnara
   ```
3. **Authenticate** in your browser (opens automatically)
4. **See everything** your agent does in the Omnara dashboard!

### Option 2: Launch Agents Remotely

Trigger Claude from your phone:

1. **Start the server** on your computer:
   ```bash
   # Using pip
   pip install omnara
   omnara serve
   
   # Using uv (faster)
   uv pip install omnara
   uv run omnara serve
   ```
2. **Set up your agent** in the mobile app with the webhook URL shown
3. **Launch agents** from anywhere - beach, coffee shop, bed!

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

## 🔧 Advanced Usage (Without CLI)

> **Note**: Most users should use the simple `omnara` or `omnara serve` commands shown above. These methods are for advanced users who need custom integrations or want to run the underlying scripts directly.

### Method 1: Direct Wrapper Script

Run the monitoring wrapper directly (what `omnara` does under the hood):

```bash
# Basic usage
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY

# With git diff tracking
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY --git-diff

# Custom API endpoint (for self-hosted)
python -m webhooks.claude_wrapper_v3 --api-key YOUR_API_KEY --base-url https://your-server.com
```

### Method 2: Manual MCP Configuration

For custom MCP setups, you can configure manually:

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

- 💬 [GitHub Discussions](https://github.com/omnara-ai/omnara/discussions)
- 🐛 [Report Issues](https://github.com/omnara-ai/omnara/issues)
- 📧 [Email Support](mailto:ishaan@omnara.com)
- 📖 Documentation (coming soon)

## 📜 License

Omnara is open source software licensed under the [Apache 2.0 License](LICENSE).

---

<div align="center">

**Built with ❤️ by the Omnara team**

[Website](https://omnara.ai) • [Twitter](https://twitter.com/omnara_ai) • [LinkedIn](https://linkedin.com/company/omnara)

</div>