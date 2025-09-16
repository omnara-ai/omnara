export type Json =
  | string
  | number
  | boolean
  | null
  | { [key: string]: Json | undefined }
  | Json[]

export type Database = {
  // Allows to automatically instanciate createClient with right options
  // instead of createClient<Database, { PostgrestVersion: 'XX' }>(URL, KEY)
  __InternalSupabase: {
    PostgrestVersion: "12.2.3 (519615d)"
  }
  public: {
    Tables: {
      agent_instances: {
        Row: {
          ended_at: string | null
          id: string
          started_at: string
          status: Database["public"]["Enums"]["agentstatus"]
          user_agent_id: string
          user_id: string
        }
        Insert: {
          ended_at?: string | null
          id: string
          started_at: string
          status: Database["public"]["Enums"]["agentstatus"]
          user_agent_id: string
          user_id: string
        }
        Update: {
          ended_at?: string | null
          id?: string
          started_at?: string
          status?: Database["public"]["Enums"]["agentstatus"]
          user_agent_id?: string
          user_id?: string
        }
        Relationships: [
          {
            foreignKeyName: "agent_instances_user_agent_id_fkey"
            columns: ["user_agent_id"]
            isOneToOne: false
            referencedRelation: "user_agents"
            referencedColumns: ["id"]
          },
          {
            foreignKeyName: "agent_instances_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      agent_questions: {
        Row: {
          agent_instance_id: string
          answer_text: string | null
          answered_at: string | null
          answered_by_user_id: string | null
          asked_at: string
          id: string
          is_active: boolean
          question_text: string
        }
        Insert: {
          agent_instance_id: string
          answer_text?: string | null
          answered_at?: string | null
          answered_by_user_id?: string | null
          asked_at: string
          id: string
          is_active: boolean
          question_text: string
        }
        Update: {
          agent_instance_id?: string
          answer_text?: string | null
          answered_at?: string | null
          answered_by_user_id?: string | null
          asked_at?: string
          id?: string
          is_active?: boolean
          question_text?: string
        }
        Relationships: [
          {
            foreignKeyName: "agent_questions_agent_instance_id_fkey"
            columns: ["agent_instance_id"]
            isOneToOne: false
            referencedRelation: "agent_instances"
            referencedColumns: ["id"]
          },
          {
            foreignKeyName: "agent_questions_answered_by_user_id_fkey"
            columns: ["answered_by_user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      agent_steps: {
        Row: {
          agent_instance_id: string
          created_at: string
          description: string
          id: string
          step_number: number
        }
        Insert: {
          agent_instance_id: string
          created_at: string
          description: string
          id: string
          step_number: number
        }
        Update: {
          agent_instance_id?: string
          created_at?: string
          description?: string
          id?: string
          step_number?: number
        }
        Relationships: [
          {
            foreignKeyName: "agent_steps_agent_instance_id_fkey"
            columns: ["agent_instance_id"]
            isOneToOne: false
            referencedRelation: "agent_instances"
            referencedColumns: ["id"]
          },
        ]
      }
      agent_user_feedback: {
        Row: {
          agent_instance_id: string
          created_at: string
          created_by_user_id: string
          feedback_text: string
          id: string
          retrieved_at: string | null
        }
        Insert: {
          agent_instance_id: string
          created_at: string
          created_by_user_id: string
          feedback_text: string
          id: string
          retrieved_at?: string | null
        }
        Update: {
          agent_instance_id?: string
          created_at?: string
          created_by_user_id?: string
          feedback_text?: string
          id?: string
          retrieved_at?: string | null
        }
        Relationships: [
          {
            foreignKeyName: "agent_user_feedback_agent_instance_id_fkey"
            columns: ["agent_instance_id"]
            isOneToOne: false
            referencedRelation: "agent_instances"
            referencedColumns: ["id"]
          },
          {
            foreignKeyName: "agent_user_feedback_created_by_user_id_fkey"
            columns: ["created_by_user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      alembic_version: {
        Row: {
          version_num: string
        }
        Insert: {
          version_num: string
        }
        Update: {
          version_num?: string
        }
        Relationships: []
      }
      api_keys: {
        Row: {
          api_key: string
          api_key_hash: string
          created_at: string
          expires_at: string | null
          id: string
          is_active: boolean
          last_used_at: string | null
          name: string
          user_id: string
        }
        Insert: {
          api_key: string
          api_key_hash: string
          created_at: string
          expires_at?: string | null
          id: string
          is_active: boolean
          last_used_at?: string | null
          name: string
          user_id: string
        }
        Update: {
          api_key?: string
          api_key_hash?: string
          created_at?: string
          expires_at?: string | null
          id?: string
          is_active?: boolean
          last_used_at?: string | null
          name?: string
          user_id?: string
        }
        Relationships: [
          {
            foreignKeyName: "api_keys_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      billing_events: {
        Row: {
          event_data: string | null
          event_type: string
          id: string
          occurred_at: string
          provider_event_id: string | null
          subscription_id: string | null
          user_id: string
        }
        Insert: {
          event_data?: string | null
          event_type: string
          id: string
          occurred_at: string
          provider_event_id?: string | null
          subscription_id?: string | null
          user_id: string
        }
        Update: {
          event_data?: string | null
          event_type?: string
          id?: string
          occurred_at?: string
          provider_event_id?: string | null
          subscription_id?: string | null
          user_id?: string
        }
        Relationships: [
          {
            foreignKeyName: "billing_events_subscription_id_fkey"
            columns: ["subscription_id"]
            isOneToOne: false
            referencedRelation: "subscriptions"
            referencedColumns: ["id"]
          },
          {
            foreignKeyName: "billing_events_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      push_tokens: {
        Row: {
          created_at: string
          id: string
          is_active: boolean
          last_used_at: string | null
          platform: string
          token: string
          updated_at: string
          user_id: string
        }
        Insert: {
          created_at: string
          id: string
          is_active: boolean
          last_used_at?: string | null
          platform: string
          token: string
          updated_at: string
          user_id: string
        }
        Update: {
          created_at?: string
          id?: string
          is_active?: boolean
          last_used_at?: string | null
          platform?: string
          token?: string
          updated_at?: string
          user_id?: string
        }
        Relationships: [
          {
            foreignKeyName: "push_tokens_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      subscriptions: {
        Row: {
          agent_limit: number
          created_at: string
          id: string
          plan_type: string
          provider_customer_id: string | null
          provider_subscription_id: string | null
          updated_at: string
          user_id: string
        }
        Insert: {
          agent_limit: number
          created_at: string
          id: string
          plan_type: string
          provider_customer_id?: string | null
          provider_subscription_id?: string | null
          updated_at: string
          user_id: string
        }
        Update: {
          agent_limit?: number
          created_at?: string
          id?: string
          plan_type?: string
          provider_customer_id?: string | null
          provider_subscription_id?: string | null
          updated_at?: string
          user_id?: string
        }
        Relationships: [
          {
            foreignKeyName: "subscriptions_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: true
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      user_agents: {
        Row: {
          created_at: string
          id: string
          is_active: boolean
          name: string
          updated_at: string
          user_id: string
          webhook_api_key: string | null
          webhook_url: string | null
        }
        Insert: {
          created_at?: string
          id: string
          is_active?: boolean
          name: string
          updated_at?: string
          user_id: string
          webhook_api_key?: string | null
          webhook_url?: string | null
        }
        Update: {
          created_at?: string
          id?: string
          is_active?: boolean
          name?: string
          updated_at?: string
          user_id?: string
          webhook_api_key?: string | null
          webhook_url?: string | null
        }
        Relationships: [
          {
            foreignKeyName: "user_agents_user_id_fkey"
            columns: ["user_id"]
            isOneToOne: false
            referencedRelation: "users"
            referencedColumns: ["id"]
          },
        ]
      }
      users: {
        Row: {
          created_at: string
          display_name: string | null
          email: string
          id: string
          updated_at: string
        }
        Insert: {
          created_at: string
          display_name?: string | null
          email: string
          id: string
          updated_at: string
        }
        Update: {
          created_at?: string
          display_name?: string | null
          email?: string
          id?: string
          updated_at?: string
        }
        Relationships: []
      }
    }
    Views: {
      [_ in never]: never
    }
    Functions: {
      [_ in never]: never
    }
    Enums: {
      agentstatus:
        | "ACTIVE"
        | "AWAITING_INPUT"
        | "PAUSED"
        | "STALE"
        | "COMPLETED"
        | "FAILED"
        | "KILLED"
        | "DISCONNECTED"
    }
    CompositeTypes: {
      [_ in never]: never
    }
  }
}

type DatabaseWithoutInternals = Omit<Database, "__InternalSupabase">

type DefaultSchema = DatabaseWithoutInternals[Extract<keyof Database, "public">]

export type Tables<
  DefaultSchemaTableNameOrOptions extends
    | keyof (DefaultSchema["Tables"] & DefaultSchema["Views"])
    | { schema: keyof DatabaseWithoutInternals },
  TableName extends DefaultSchemaTableNameOrOptions extends {
    schema: keyof DatabaseWithoutInternals
  }
    ? keyof (DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"] &
        DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Views"])
    : never = never,
> = DefaultSchemaTableNameOrOptions extends {
  schema: keyof DatabaseWithoutInternals
}
  ? (DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"] &
      DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Views"])[TableName] extends {
      Row: infer R
    }
    ? R
    : never
  : DefaultSchemaTableNameOrOptions extends keyof (DefaultSchema["Tables"] &
        DefaultSchema["Views"])
    ? (DefaultSchema["Tables"] &
        DefaultSchema["Views"])[DefaultSchemaTableNameOrOptions] extends {
        Row: infer R
      }
      ? R
      : never
    : never

export type TablesInsert<
  DefaultSchemaTableNameOrOptions extends
    | keyof DefaultSchema["Tables"]
    | { schema: keyof DatabaseWithoutInternals },
  TableName extends DefaultSchemaTableNameOrOptions extends {
    schema: keyof DatabaseWithoutInternals
  }
    ? keyof DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"]
    : never = never,
> = DefaultSchemaTableNameOrOptions extends {
  schema: keyof DatabaseWithoutInternals
}
  ? DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"][TableName] extends {
      Insert: infer I
    }
    ? I
    : never
  : DefaultSchemaTableNameOrOptions extends keyof DefaultSchema["Tables"]
    ? DefaultSchema["Tables"][DefaultSchemaTableNameOrOptions] extends {
        Insert: infer I
      }
      ? I
      : never
    : never

export type TablesUpdate<
  DefaultSchemaTableNameOrOptions extends
    | keyof DefaultSchema["Tables"]
    | { schema: keyof DatabaseWithoutInternals },
  TableName extends DefaultSchemaTableNameOrOptions extends {
    schema: keyof DatabaseWithoutInternals
  }
    ? keyof DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"]
    : never = never,
> = DefaultSchemaTableNameOrOptions extends {
  schema: keyof DatabaseWithoutInternals
}
  ? DatabaseWithoutInternals[DefaultSchemaTableNameOrOptions["schema"]]["Tables"][TableName] extends {
      Update: infer U
    }
    ? U
    : never
  : DefaultSchemaTableNameOrOptions extends keyof DefaultSchema["Tables"]
    ? DefaultSchema["Tables"][DefaultSchemaTableNameOrOptions] extends {
        Update: infer U
      }
      ? U
      : never
    : never

export type Enums<
  DefaultSchemaEnumNameOrOptions extends
    | keyof DefaultSchema["Enums"]
    | { schema: keyof DatabaseWithoutInternals },
  EnumName extends DefaultSchemaEnumNameOrOptions extends {
    schema: keyof DatabaseWithoutInternals
  }
    ? keyof DatabaseWithoutInternals[DefaultSchemaEnumNameOrOptions["schema"]]["Enums"]
    : never = never,
> = DefaultSchemaEnumNameOrOptions extends {
  schema: keyof DatabaseWithoutInternals
}
  ? DatabaseWithoutInternals[DefaultSchemaEnumNameOrOptions["schema"]]["Enums"][EnumName]
  : DefaultSchemaEnumNameOrOptions extends keyof DefaultSchema["Enums"]
    ? DefaultSchema["Enums"][DefaultSchemaEnumNameOrOptions]
    : never

export type CompositeTypes<
  PublicCompositeTypeNameOrOptions extends
    | keyof DefaultSchema["CompositeTypes"]
    | { schema: keyof DatabaseWithoutInternals },
  CompositeTypeName extends PublicCompositeTypeNameOrOptions extends {
    schema: keyof DatabaseWithoutInternals
  }
    ? keyof DatabaseWithoutInternals[PublicCompositeTypeNameOrOptions["schema"]]["CompositeTypes"]
    : never = never,
> = PublicCompositeTypeNameOrOptions extends {
  schema: keyof DatabaseWithoutInternals
}
  ? DatabaseWithoutInternals[PublicCompositeTypeNameOrOptions["schema"]]["CompositeTypes"][CompositeTypeName]
  : PublicCompositeTypeNameOrOptions extends keyof DefaultSchema["CompositeTypes"]
    ? DefaultSchema["CompositeTypes"][PublicCompositeTypeNameOrOptions]
    : never

export const Constants = {
  public: {
    Enums: {
      agentstatus: [
        "ACTIVE",
        "AWAITING_INPUT",
        "PAUSED",
        "STALE",
        "COMPLETED",
        "FAILED",
        "KILLED",
        "DISCONNECTED",
      ],
    },
  },
} as const
