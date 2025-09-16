
import {
  AgentType,
  InstanceDetail,
  AgentInstance,
  Message,
  APIKey,
  NewAPIKey,
  AgentStatus,
  AgentQuestion,
  InstanceShare,
  InstanceAccessLevel,
  /*, CostAnalytics*/
} from '../types/dashboard'
import { toast } from '../hooks/use-toast'
import { supabase } from './supabase'
import { authClient } from './auth/authClient'

// const API_BASE_URL = import.meta.env.VITE_ENVIRONMENT == "production"
//   ? import.meta.env.VITE_API_URL 
//   : 'http://localhost:8000'
const API_BASE_URL = import.meta.env.VITE_API_URL || 'https://agent-dashboard-backend.onrender.com'

class ApiClient {
  public get baseUrl(): string {
    return API_BASE_URL
  }
  private async getAuthToken(): Promise<string | null> {
    const { data: { session } } = await supabase.auth.getSession()
    return session?.access_token || null
  }

  private async request<T>(endpoint: string, options?: RequestInit, attempt: number = 0): Promise<T> {
    try {
      const token = await this.getAuthToken()
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
      }
      
      // Add existing headers
      if (options?.headers) {
        Object.assign(headers, options.headers)
      }
      
      if (token) {
        headers['Authorization'] = `Bearer ${token}`
      }

      const response = await fetch(`${API_BASE_URL}${endpoint}`, {
        headers,
        // Avoid sending cross-site cookies to prevent stale-cookie auth conflicts
        // (Bearer token via Authorization header is the source of truth)
        ...options,
      })

      if (!response.ok) {
        // Try to parse error response body
        let errorData: any = null
        try {
          errorData = await response.json()
        } catch {
          // If parsing fails, errorData remains null
        }

        if (response.status === 401) {
          // Minimal fix: retry once on 401 after a short backoff to
          // handle race conditions right after login/user provisioning.
          if (attempt === 0) {
            await new Promise(res => setTimeout(res, 400))
            return this.request<T>(endpoint, options, attempt + 1)
          }
          toast({
            title: "Authentication Required",
            description: "Please sign in to access the dashboard.",
            variant: "destructive",
          })
          // Remote-first sign-out; token clear happens inside signOut
          await authClient.signOut().catch(() => {})
          window.location.href = '/'
          throw new Error('Authentication required')
        }
        
        // Extract error message from response body
        let errorMessage = response.statusText
        if (errorData) {
          if (typeof errorData === 'string') {
            errorMessage = errorData
          } else if (errorData.message) {
            errorMessage = errorData.message
          } else if (errorData.detail) {
            // Handle both string and object detail formats
            if (typeof errorData.detail === 'string') {
              errorMessage = errorData.detail
            } else if (errorData.detail.message) {
              errorMessage = errorData.detail.message
            }
          } else if (errorData.error) {
            errorMessage = errorData.error
          }
        }

        // Show specific error message from server
        if (response.status >= 500) {
          toast({
            title: "Server Error",
            description: errorMessage || "Something went wrong on our end. Please try again later.",
            variant: "destructive",
          })
        } else if (response.status === 424) {
          // Webhook not available - show as info, not error
          toast({
            title: "Webhook Not Available",
            description: errorMessage || "Please check that your agent is running.",
            variant: "default",  // White/normal toast, not red
          })
        } else {
          toast({
            title: "Request Failed",
            description: errorMessage,
            variant: "destructive",
          })
        }
        
        // Throw error with a marker that we already showed a toast
        const error = new Error(errorMessage || `API request failed: ${response.statusText}`)
        ;(error as any).toastShown = true
        throw error
      }

      return response.json()
    } catch (error) {
      // Only show network error toast if it's an actual network failure (fetch failed)
      // Don't show it for HTTP errors we already handled above
      if (error instanceof Error && 
          error.message !== 'Authentication required' &&
          !(error as any).toastShown) {
        toast({
          title: "Network Error",
          description: "Unable to connect to the server. Please check your connection.",
          variant: "destructive",
        })
      }
      throw error
    }
  }


  async getAgentTypes(): Promise<AgentType[]> {
    return await this.request<AgentType[]>(`/api/v1/agent-types`)
  }

  async getAllInstances(limit?: number, scope: 'me' | 'shared' | 'all' = 'me'): Promise<AgentInstance[]> {
    const params = new URLSearchParams()
    if (limit !== undefined) {
      params.append('limit', limit.toString())
    }
    params.append('scope', scope)
    const query = params.toString()
    return await this.request<AgentInstance[]>(`/api/v1/agent-instances${query ? `?${query}` : ''}`)
  }

  async getAgentTypeInstances(typeId: string): Promise<AgentInstance[]> {
    return await this.request<AgentInstance[]>(`/api/v1/agent-types/${typeId}/instances`)
  }

  async getInstanceDetail(instanceId: string, messageLimit?: number, beforeMessageId?: string): Promise<InstanceDetail> {
    const params = new URLSearchParams()
    if (messageLimit !== undefined) params.append('message_limit', messageLimit.toString())
    if (beforeMessageId) params.append('before_message_id', beforeMessageId)
    const queryString = params.toString()
    return await this.request<InstanceDetail>(`/api/v1/agent-instances/${instanceId}${queryString ? '?' + queryString : ''}`)
  }

  async getInstanceMessages(instanceId: string, limit: number = 50, beforeMessageId?: string): Promise<Message[]> {
    const params = new URLSearchParams()
    params.append('limit', limit.toString())
    if (beforeMessageId) params.append('before_message_id', beforeMessageId)
    return await this.request<Message[]>(`/api/v1/agent-instances/${instanceId}/messages?${params.toString()}`)
  }

  async submitUserMessage(instanceId: string, content: string): Promise<Message> {
    return this.request<Message>(`/api/v1/agent-instances/${instanceId}/messages`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    })
  }

  async getMessageStreamUrl(instanceId: string): Promise<string | null> {
    const token = await this.getAuthToken()
    if (!token) {
      console.error('No auth token available for SSE')
      return null
    }

    // Since EventSource doesn't support headers, we'll pass token as query param
    // Note: This is less secure but standard practice for SSE
    return `${this.baseUrl}/api/v1/agent-instances/${instanceId}/messages/stream?token=${encodeURIComponent(token)}`
  }

  async updateInstanceStatus(instanceId: string, status: AgentStatus): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/status`, {
      method: 'PUT',
      body: JSON.stringify({ status }),
    })
  }

  async pauseAgent(instanceId: string): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/pause`, {
      method: 'POST',
    })
  }

  async resumeAgent(instanceId: string): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/resume`, {
      method: 'POST',
    })
  }

  async killAgent(instanceId: string): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/kill`, {
      method: 'POST',
    })
  }

  // async interruptAgent(instanceId: string): Promise<void> {
  //   await this.request(`/api/v1/agent-instances/${instanceId}/interrupt`, {
  //     method: 'POST',
  //   })
  // }

  async deleteAgentInstance(instanceId: string): Promise<{ message: string }> {
    return this.request(`/api/v1/agent-instances/${instanceId}`, {
      method: 'DELETE',
    })
  }

  async getInstanceAccessList(instanceId: string): Promise<InstanceShare[]> {
    return this.request<InstanceShare[]>(`/api/v1/agent-instances/${instanceId}/access`)
  }

  async addInstanceShare(
    instanceId: string,
    payload: { email: string; access: InstanceAccessLevel }
  ): Promise<InstanceShare> {
    return this.request<InstanceShare>(`/api/v1/agent-instances/${instanceId}/access`, {
      method: 'POST',
      body: JSON.stringify(payload),
    })
  }

  async removeInstanceShare(instanceId: string, shareId: string): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/access/${shareId}`, {
      method: 'DELETE',
    })
  }

  async updateAgentInstance(instanceId: string, data: { name: string }): Promise<AgentInstance> {
    return this.request(`/api/v1/agent-instances/${instanceId}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    })
  }

  async createAPIKey(name: string, expiresInDays: number | null): Promise<NewAPIKey> {
    return this.request('/api/v1/auth/api-keys', {
      method: 'POST',
      body: JSON.stringify({ 
        name, 
        expires_in_days: expiresInDays 
      }),
    })
  }

  async createCLIKey(): Promise<{ api_key: string }> {
    return this.request('/api/v1/auth/cli-key', {
      method: 'POST',
    })
  }

  async getAPIKeys(): Promise<APIKey[]> {
    return this.request('/api/v1/auth/api-keys')
  }

  async revokeAPIKey(keyId: string): Promise<{ message: string }> {
    return this.request(`/api/v1/auth/api-keys/${keyId}`, {
      method: 'DELETE',
    })
  }


  // async getCostAnalytics(): Promise<CostAnalytics> {
  //   return this.request<CostAnalytics>('/api/v1/analytics/costs')
  // }

  async getAgentSummary(): Promise<{
    total_instances: number
    active_instances: number
    completed_instances: number
    agent_types: Array<{
      id: string
      name: string
      total_instances: number
      active_instances: number
    }>
  }> {
    return this.request('/api/v1/agent-summary')
  }

  // Webhook Types
  async getWebhookTypes(): Promise<any[]> {
    return this.request('/api/v1/user-agents/webhook-types')
  }

  // User Agent Management  
  async getUserAgents(): Promise<any[]> {
    return this.request('/api/v1/user-agents')
  }

  async createUserAgent(data: {
    name: string
    webhook_type?: string
    webhook_config?: Record<string, any>
    webhook_url?: string  // Keep for backward compatibility
    webhook_api_key?: string  // Keep for backward compatibility
    is_active: boolean
  }): Promise<any> {
    // Convert old format to new format if needed
    const payload = { ...data }
    if (!payload.webhook_type && payload.webhook_url) {
      payload.webhook_type = 'DEFAULT'
      payload.webhook_config = {
        url: payload.webhook_url,
        api_key: payload.webhook_api_key
      }
    }
    return this.request('/api/v1/user-agents', {
      method: 'POST',
      body: JSON.stringify(payload),
    })
  }

  async updateUserAgent(id: string, data: {
    name: string
    webhook_type?: string
    webhook_config?: Record<string, any>
    webhook_url?: string  // Keep for backward compatibility
    webhook_api_key?: string  // Keep for backward compatibility
    is_active: boolean
  }): Promise<any> {
    // Convert old format to new format if needed
    const payload = { ...data }
    if (!payload.webhook_type && payload.webhook_url) {
      payload.webhook_type = 'DEFAULT'
      payload.webhook_config = {
        url: payload.webhook_url,
        api_key: payload.webhook_api_key
      }
    }
    return this.request(`/api/v1/user-agents/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(payload),
    })
  }

  async deleteUserAgent(id: string): Promise<{ message: string }> {
    return this.request(`/api/v1/user-agents/${id}`, {
      method: 'DELETE',
    })
  }

  async createAgentInstance(agentId: string, prompt: string, name?: string, worktreeName?: string, branchName?: string): Promise<{
    success: boolean
    agent_instance_id?: string
    message: string
    error?: string
  }> {
    return this.request(`/api/v1/user-agents/${agentId}/instances`, {
      method: 'POST',
      body: JSON.stringify({ prompt, name, worktree_name: worktreeName, branch_name: branchName }),
    })
  }

  async getUserAgentInstances(agentId: string): Promise<AgentInstance[]> {
    return await this.request<AgentInstance[]>(`/api/v1/user-agents/${agentId}/instances`)
  }

  // Notification settings endpoints
  async getNotificationSettings(): Promise<any> {
    return this.request('/api/v1/user/notification-settings')
  }

  async updateNotificationSettings(settings: any): Promise<any> {
    return this.request('/api/v1/user/notification-settings', {
      method: 'PUT',
      body: JSON.stringify(settings),
    })
  }

  async testNotification(type: string): Promise<any> {
    return this.request(`/api/v1/user/test-notification?notification_type=${type}`, {
      method: 'POST',
    })
  }

  async answerQuestion(instanceId: string, questionId: string, answer: any): Promise<void> {
    await this.request(`/api/v1/agent-instances/${instanceId}/questions/${questionId}/answer`, {
      method: 'POST',
      body: JSON.stringify({ answer }),
    })
  }

  // Generic methods for other endpoints
  get<T>(endpoint: string): Promise<T> {
    return this.request<T>(endpoint)
  }

  put<T>(endpoint: string, data: any): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'PUT',
      body: JSON.stringify(data),
    })
  }

  post<T>(endpoint: string, data?: any): Promise<T> {
    return this.request<T>(endpoint, {
      method: 'POST',
      body: data ? JSON.stringify(data) : undefined,
    })
  }
}

export const apiClient = new ApiClient()
