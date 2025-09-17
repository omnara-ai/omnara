export enum AgentStatus {
  ACTIVE = 'ACTIVE',
  AWAITING_INPUT = 'AWAITING_INPUT',
  PAUSED = 'PAUSED',
  COMPLETED = 'COMPLETED',
  FAILED = 'FAILED',
  KILLED = 'KILLED'
}

export interface UserProfile {
  id: string;
  email: string;
  display_name: string | null;
  created_at: string;
}

export interface AgentType {
  id: string;
  name: string;
  created_at: string;
  recent_instances: AgentInstance[];
}

export interface AgentInstance {
  id: string;
  agent_type_id: string;
  status: AgentStatus;
  started_at: string;
  ended_at?: string;
  latest_message?: string;
  latest_message_at?: string;
  chat_length?: number;
  last_signal_at?: string;
  total_cost_usd?: number;
  agent_type_name?: string;
  name?: string;
}

export interface Message {
  id: string;
  content: string;
  sender_type: 'AGENT' | 'USER';
  created_at: string;
  requires_user_input: boolean;
}

export interface InstanceDetail extends AgentInstance {
  messages: Message[];
  git_diff?: string | null;
  last_read_message_id?: string | null;
}


export interface APIKey {
  id: string;
  name: string;
  api_key: string;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  is_active: boolean;
}

export interface NewAPIKey {
  id: string;
  name: string;
  api_key: string;
  created_at: string;
  expires_at: string | null;
}