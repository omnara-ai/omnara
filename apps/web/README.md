# Omnara Agent Command Center

The sophisticated command center for AI agents - monitor, manage, and collaborate with your entire fleet of AI agents from a single, unified dashboard.

## 🚀 Features

### Landing Page
- **Sophisticated Design**: Professional landing page with glassmorphism effects, light beam animations, and gradient aesthetics
- **Y Combinator Badge**: Proudly displaying Y Combinator backing
- **Interactive Product Mockup**: Dynamic dashboard preview with real-time animations
- **Early Access Integration**: Seamless form integration for user onboarding
- **Responsive Design**: Mobile-first approach with perfect scaling across all devices

### Dashboard
- **Real-time Agent Monitoring**: Track your AI agents' status, progress, and activities with live polling
- **Interactive Timeline**: Visual timeline of agent steps, questions, and user feedback
- **Question & Answer System**: Handle agent questions and provide real-time feedback
- **API Key Management**: Secure MCP (Model Context Protocol) authentication system
- **Professional UI/UX**: Y Combinator startup-quality design with sophisticated animations

### Technical Excellence
- **Unified Design System**: Consistent midnight-blue to electric-blue aesthetic across all components
- **Authentication Integration**: Custom auth with protected routes
- **Performance Optimized**: Efficient polling, caching, and smooth animations
- **Type Safety**: Full TypeScript coverage with comprehensive type definitions

## 🛠 Tech Stack

### Frontend
- **React 18** with TypeScript
- **Vite** for lightning-fast development
- **React Router v6** for sophisticated routing
- **Tailwind CSS** with custom design system
- **Radix UI** for accessible components

### Backend Integration
- **Custom Authentication** with cookie-based sessions
- **RESTful API Client** with error handling
- **Real-time Polling** for live updates
- **Toast Notifications** for user feedback

### Design & Animation
- **Glassmorphism Effects** with backdrop blur
- **Light Beam Animations** for visual depth
- **Gradient Text Treatments** for premium feel
- **Staggered Animation Timing** for polished interactions

## 🚀 Quick Start

### Prerequisites
- Node.js 18+
- npm or yarn
- Backend API server

### Installation

1. **Clone and Install**
```bash
git clone https://github.com/your-org/omnara-agent-command.git
cd omnara-agent-command
npm install
```

2. **Environment Setup**
```bash
cp env.example .env.local
```

Configure your environment variables (see Environment Configuration below).

3. **Development Server**
```bash
npm run dev
```

Visit `http://localhost:5173` to see your command center in action!

## ⚙️ Environment Configuration

Create `.env.local` based on `env.example`:

```bash
# API Configuration (Required)
VITE_API_URL=https://api.omnara.com

# Environment
VITE_ENVIRONMENT=production

# Optional: Analytics & Monitoring
VITE_ANALYTICS_ID=your-analytics-id
VITE_SENTRY_DSN=your-sentry-dsn

# Optional: Feature Flags
VITE_ENABLE_DASHBOARD=true
VITE_ENABLE_EARLY_ACCESS=true
```

## 🏗 Project Structure

```
omnara-agent-command/
├── src/
│   ├── components/
│   │   ├── dashboard/           # Dashboard components
│   │   │   ├── AgentGrid.tsx   # Main agent overview
│   │   │   ├── AgentCard.tsx   # Individual agent cards
│   │   │   ├── InstanceList.tsx # Agent instance list
│   │   │   ├── InstanceDetail.tsx # Detailed instance view
│   │   │   ├── APIKeyManagement.tsx # API key management
│   │   │   └── DashboardLayout.tsx # Dashboard wrapper
│   │   ├── ui/                 # Reusable UI components
│   │   ├── HeroSection.tsx     # Landing page hero
│   │   ├── Header.tsx          # Navigation header
│   │   └── ...                 # Other landing components
│   ├── lib/
│   │   ├── auth/              # Authentication system
│   │   ├── dashboardApi.ts    # API client
│   │   └── utils.ts           # Utilities
│   ├── types/
│   │   └── dashboard.ts       # TypeScript definitions
│   └── hooks/
│       └── usePolling.ts      # Real-time polling
├── env.example                # Environment template
└── README.md
```

## 🎨 Design System

### Color Palette
- **midnight-blue**: Primary background (#1e3a8a)
- **electric-blue**: Secondary gradient (#3b82f6)
- **electric-accent**: Accent color (#60a5fa)
- **off-white**: Text color (#f8fafc)

### Components
- **Glassmorphism**: `bg-white/10 backdrop-blur-md border-white/20`
- **Gradient Text**: `bg-gradient-to-r from-white to-electric-accent bg-clip-text text-transparent`
- **Hover Effects**: `hover:scale-[1.02] transition-all duration-300`

## 🚀 Deployment

### Vercel (Recommended)
```bash
npm run build
```

1. Connect repository to Vercel
2. Add environment variables in Vercel dashboard
3. Deploy automatically on push

### Other Platforms
- **Netlify**: Works out of the box
- **AWS Amplify**: Perfect for enterprise
- **CloudFlare Pages**: Great performance
- **Any static host**: Universal compatibility

### Build Commands
- **Build**: `npm run build`
- **Preview**: `npm run preview`
- **Lint**: `npm run lint`

## 🔧 Development

### Phase-by-Phase Migration (Completed)
- ✅ **Phase 1**: Project Structure & Dependencies
- ✅ **Phase 2**: Design System Integration  
- ✅ **Phase 3**: Routing & Navigation Integration
- ✅ **Phase 4**: Authentication & State Management
- ✅ **Phase 5**: Design Enhancement
- ✅ **Phase 6**: Performance & Polish

### Key Features Implemented
- Unified routing between landing page and dashboard
- Protected routes with custom authentication
- Real-time polling for agent updates
- Comprehensive error handling with toast notifications
- Professional loading states and animations
- Mobile-responsive design throughout

## 🤝 Contributing

1. Fork the repository
2. Create feature branch: `git checkout -b feature/amazing-feature`
3. Commit changes: `git commit -m 'Add amazing feature'`
4. Push to branch: `git push origin feature/amazing-feature`
5. Open a Pull Request

## 📝 License

MIT License - see [LICENSE](LICENSE) for details.

## 🏢 About Omnara

Built by AI engineers from Meta, Microsoft, and Amazon. Backed by Y Combinator.

Turn passive monitoring into active collaboration with your AI agents.
