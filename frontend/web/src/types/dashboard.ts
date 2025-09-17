export enum AgentStatus {
  ACTIVE = 'ACTIVE',
  AWAITING_INPUT = 'AWAITING_INPUT',
  PAUSED = 'PAUSED',
  COMPLETED = 'COMPLETED',
  FAILED = 'FAILED',
  KILLED = 'KILLED'
}

export enum InstanceAccessLevel {
  READ = 'READ',
  WRITE = 'WRITE'
}

export interface UserProfile {
    id: string
    email: string
    display_name: string | null
    created_at: string
  }

export interface AgentType {
  id: string
  name: string
  created_at: string
  recent_instances: AgentInstance[]
}

export interface AgentInstance {
  id: string
  agent_type_id: string
  status: AgentStatus
  started_at: string
  ended_at?: string
  latest_message?: string
  latest_message_at?: string
  chat_length?: number
  last_heartbeat_at?: string
  last_signal_at?: string
  total_cost_usd?: number
  agent_type_name?: string
  name?: string
  created_at?: string
}

export interface Message {
  id: string
  content: string
  sender_type: 'AGENT' | 'USER'
  created_at: string
  requires_user_input: boolean
  sender_user_id?: string | null
  sender_user_email?: string | null
  sender_user_display_name?: string | null
}

export interface AgentQuestion {
  id: string
  question: string
  question_type: 'yes_no' | 'multiple_choice' | 'text'
  options?: string[]
  created_at: string
}

export interface AgentStep {
  id: string
  step_number: number
  description: string
  status: 'pending' | 'in_progress' | 'completed' | 'failed'
  created_at: string
  completed_at?: string
}

export interface UserFeedback {
  id: string
  rating: number
  comment?: string
  created_at: string
}

export interface InstanceDetail extends AgentInstance {
  messages: Message[]
  git_diff?: string | null
  last_read_message_id?: string | null
  questions?: AgentQuestion[]
  agent_type?: string
  access_level: InstanceAccessLevel
  is_owner: boolean
}

export interface InstanceShare {
  id: string
  email: string
  access: InstanceAccessLevel
  user_id?: string | null
  display_name?: string | null
  invited: boolean
  is_owner: boolean
  created_at: string
  updated_at: string
}

export interface APIKey {
  id: string
  name: string
  api_key: string
  created_at: string
  expires_at: string | null
  last_used_at: string | null
  is_active: boolean
}

export interface NewAPIKey {
  id: string
  name: string
  api_key: string
  created_at: string
  expires_at: string | null
}

// Cost tracking types
/* export interface CostAnalytics {
  total_cost_last_30_days: number
  total_instances_last_30_days: number
  cost_by_agent_type: Array<{
    agent_type_name: string
    total_cost: number
  }>
  cost_by_model: Array<{
    model_name: string
    total_cost: number
  }>
  daily_costs: Array<{
    date: string
    cost: number
  }>
  most_expensive_instances: Array<{
    id: string
    agent_type_name: string
    total_cost: number
    started_at: string
  }>
} */
