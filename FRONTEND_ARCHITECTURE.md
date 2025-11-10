# Omnara Web Application Frontend Architecture Summary

## Technology Stack

### Core Framework
- **Framework**: Vite-based React 18 (NOT Next.js)
- **Routing**: React Router v6 (client-side)
- **Build Tool**: Vite 5
- **Language**: TypeScript 5.5
- **Styling**: TailwindCSS 3.4 + shadcn/ui components (Radix UI)
- **Package Manager**: Bun/npm

### Data Management
- **Server State**: React Query (TanStack Query v5) with caching
- **Client State**: React Context API (AuthContext, ThemeProvider)
- **Local State**: React hooks (useState, useRef, useEffect)
- **Form Handling**: React Hook Form v7 + Zod v3 validation

### API & Real-Time Communication
- **Backend Communication**: Fetch API with custom ApiClient
- **Real-Time Updates**: Server-Sent Events (SSE) for streaming messages
- **Authentication**: Supabase Auth (JWT tokens passed as Bearer in Authorization header)
- **Base URL**: Configurable via VITE_API_URL environment variable

### Additional Libraries
- **Markdown**: react-markdown with remark plugins (remark-gfm, remark-breaks)
- **UI Components**: Radix UI primitives wrapped in shadcn/ui
- **Icons**: lucide-react
- **Toast Notifications**: sonner
- **Code Terminal**: xterm (terminal emulation for agent output)
- **Charts**: recharts
- **Analytics**: Sentry, PostHog
- **Billing**: Stripe integration

## Project Structure

```
apps/web/src/
├── pages/                    # Top-level page components
│   ├── Index.tsx            # Landing page
│   ├── CommandCenter.tsx    # Main dashboard
│   ├── AllInstances.tsx     # All instances list
│   ├── UserAgents.tsx
│   ├── CLIAuth.tsx
│   └── dashboard/
│       ├── Settings.tsx
│       └── Billing.tsx
│
├── components/
│   ├── dashboard/           # Dashboard-specific components
│   │   ├── SidebarDashboardLayout.tsx
│   │   ├── CommandPalette.tsx
│   │   ├── RecentActivity.tsx
│   │   ├── KPICards.tsx
│   │   ├── LaunchAgentModal.tsx
│   │   ├── WebhookConfigModal.tsx
│   │   ├── APIKeyManagement.tsx
│   │   ├── instances/       # Instance detail components
│   │   │   ├── InstanceDetail.tsx (SSE integration here)
│   │   │   ├── InstanceHeader.tsx
│   │   │   ├── InstanceList.tsx
│   │   │   ├── InstanceTable.tsx
│   │   │   ├── TerminalInstancePanel.tsx
│   │   │   └── TerminalLiveTerminal.tsx
│   │   │
│   │   ├── chat/            # Chat interface components
│   │   │   ├── ChatInterface.tsx
│   │   │   ├── ChatInput.tsx
│   │   │   ├── ChatMessage.tsx
│   │   │   └── ChatWorkingIndicator.tsx
│   │   │
│   │   ├── questions/       # Agent question/prompt UI
│   │   │   ├── HumanInputRequired.tsx
│   │   │   ├── StructuredQuestion.tsx
│   │   │   ├── YesNoQuestion.tsx
│   │   │   ├── OptionsQuestion.tsx
│   │   │   └── FeedbackForm.tsx
│   │   │
│   │   ├── git-diff/        # Git diff viewing
│   │   │   ├── GitDiffView.tsx
│   │   │   ├── GitDiffReviewPanel.tsx
│   │   │   ├── GitDiffStatusBar.tsx
│   │   │   └── FileTreeView.tsx
│   │   │
│   │   └── agents/          # Agent management
│   │       ├── AgentGrid.tsx
│   │       ├── AgentCard.tsx
│   │       ├── AgentManagementHub.tsx
│   │       └── UserAgentConfig.tsx
│   │
│   ├── ui/                  # shadcn/ui components (40+ components)
│   │   └── [button, input, dialog, form, table, etc.]
│   │
│   └── landing/             # Landing page components
│
├── lib/
│   ├── auth/                # Authentication
│   │   ├── AuthContext.tsx
│   │   ├── authClient.ts
│   │   └── ProtectedRoute.tsx
│   │
│   ├── theme/               # Theme management
│   │   ├── ThemeProvider.tsx
│   │   └── colors.ts
│   │
│   ├── dashboardApi.ts      # Main API client
│   ├── supabase.ts
│   ├── stripe.ts
│   └── [utilities]
│
├── hooks/
│   ├── usePolling.ts
│   ├── useSubscription.ts
│   └── use-mobile.tsx
│
├── types/
│   └── dashboard.ts         # TypeScript interfaces
│
├── utils/
│   ├── questionParser.ts
│   ├── questionScrubber.ts
│   ├── statusUtils.ts
│   └── [utilities]
│
└── App.tsx                  # Root component with routing
```

## Routing Architecture

**Router Type**: React Router v6 (Client-side)

### Route Structure:
```
/ (landing)
├── /pricing, /privacy, /terms
├── /cli-auth
│
└── /dashboard (protected)
    ├── / (CommandCenter)
    ├── /instances (AllInstances)
    ├── /instances/:instanceId (InstanceDetail - main chat interface)
    ├── /user-agents/:agentId/instances
    ├── /api-keys
    ├── /billing
    └── /settings
```

## State Management

### Global State (Context API)
1. **AuthContext**: User profile, auth methods, session
2. **ThemeProvider**: Dark mode, colors

### Server State (React Query)
- **Query Keys**: `agent-types` (5s), `user-agents` (5s), `agent-summary` (5s)
- **Caching**: 5m for subscription, 1m for usage
- **Auto-refetch**: Stale data triggers refetch

### Component Local State
- `useState` for UI state, forms, modals
- `useRef` for scroll position, event source connections

## API Integration

### ApiClient Class (`lib/dashboardApi.ts`)
- **Auth**: Supabase JWT via Authorization header
- **Error Handling**: Toast + Sentry reporting
- **Endpoints**:
  - `/api/v1/agent-types`
  - `/api/v1/agent-instances` (GET, POST, PATCH, DELETE)
  - `/api/v1/agent-instances/:id/messages` (POST)
  - `/api/v1/agent-instances/:id/messages/stream` (SSE)
  - `/api/v1/agent-instances/:id/status` (PUT)
  - `/api/v1/agent-instances/:id/pause|resume|kill` (POST)
  - `/api/v1/agent-instances/:id/access` (GET, POST, DELETE)
  - `/api/v1/auth/api-keys` (GET, POST, DELETE)
  - `/api/v1/user-agents` (GET, POST, PATCH, DELETE)

## Real-Time Updates (SSE)

**Location**: `InstanceDetail.tsx` (lines 91-185)

**Event Types**:
1. **`message`**: New chat message
   - Auto-deduplication by message ID
   - Updates chat display
   - Updates `requires_user_input` flag

2. **`status_update`**: Agent status change
   - Updates instance status badge

3. **`message_update`**: Message metadata changes
   - Updates `requires_user_input` on specific messages

4. **`heartbeat`**: Connection keep-alive

**Features**:
- Connection cleanup on unmount
- Automatic message deduplication
- Scroll position preservation
- Error reporting to Sentry

## Key Component Flows

### InstanceDetail (Agent Conversation)
```
InstanceDetail (manages SSE, fetches initial data)
├── InstanceHeader (status, title, controls)
├── ChatInterface (manages message pagination & grouping)
│   ├── ChatMessage (message rendering with markdown)
│   └── ChatInput (handles message submission)
├── TerminalInstancePanel (agent terminal output)
└── GitDiffView (code changes)
```

### Message Flow
1. User types in ChatInput
2. Submit triggers `submitUserMessage(instanceId, content)`
3. API posts message to backend
4. Backend adds to queue OR processes immediately
5. SSE stream sends message back to all connected clients
6. Message appears in ChatInterface
7. If agent response required: `requires_user_input = true`

## UI Patterns

### Message Display
- Grouped by sender + timestamp (5min threshold)
- Agent: full width, subtle background
- User: right-aligned, blue highlight
- Markdown with syntax highlighting
- "Waiting for response..." overlay on input

### Agent Questions
- Parsed from message content
- Types: yes/no, multiple choice, open-ended
- Inline UI helpers for quick responses
- Fallback to text input

### Status Indicators
- Color-coded (green=active, amber=awaiting, gray=done, red=error)
- Timestamps ("2 hours ago")
- Heartbeat tracking

## Performance Features

1. **Code Splitting**: Lazy routes
2. **Data Caching**: React Query with stale times
3. **Message Pagination**: Cursor-based, 50/page, load-on-scroll
4. **Message Deduplication**: SSE + local state
5. **Scroll Preservation**: On pagination
6. **Memory Cleanup**: SSE cleanup on unmount

## Styling System

- **TailwindCSS**: Utility-first styling
- **CSS Variables**: HSL-based colors (dark mode support)
- **Glassmorphism**: Backdrop blur, transparency
- **Animations**: Via `tailwindcss-animate`
- **Components**: shadcn/ui + Radix UI primitives

## Data Types

Key types from `types/dashboard.ts`:

```typescript
enum AgentStatus { ACTIVE, AWAITING_INPUT, PAUSED, COMPLETED, FAILED, KILLED }
enum InstanceAccessLevel { READ, WRITE }

interface Message {
  id: string
  content: string
  sender_type: 'AGENT' | 'USER'
  created_at: string
  requires_user_input: boolean
  sender_user_id?: string | null
  sender_user_email?: string | null
  sender_user_display_name?: string | null
}

interface InstanceDetail extends AgentInstance {
  messages: Message[]
  git_diff?: string | null
  last_read_message_id?: string | null
  access_level: InstanceAccessLevel
  is_owner: boolean
}

interface InstanceShare {
  id: string
  email: string
  access: InstanceAccessLevel
  user_id?: string | null
  display_name?: string | null
  invited: boolean
  is_owner: boolean
  created_at: string
}
```

## Authentication

1. Supabase Auth (OAuth2 or email)
2. JWT token via Authorization header
3. `<ProtectedRoute>` HOC checks AuthContext
4. Automatic session refresh via Supabase listener
5. 401 retry logic with backoff

## Environment Variables

```
VITE_API_URL              # Backend API
VITE_ENVIRONMENT          # dev/prod
VITE_SUPABASE_URL         # Supabase project
VITE_SUPABASE_ANON_KEY    # Public key
VITE_STRIPE_PUBLISHABLE_KEY
VITE_POSTHOG_API_KEY
VITE_SENTRY_DSN
```

## Notable Gaps for Queue Feature

Currently missing/to implement:
1. No queue preview/status display
2. No queue reordering UI
3. No message priority indicators
4. No estimated wait time
5. No "queued" status badge
6. No queue analytics
7. No cancel/remove from queue UI

