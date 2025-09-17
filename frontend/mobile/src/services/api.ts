import { supabase } from '@/lib/supabase';
import {
  AgentType,
  AgentInstance,
  InstanceDetail,
  APIKey,
  NewAPIKey,
  UserProfile,
} from '@/types';
import { reportError, reportMessage } from '@/lib/sentry';

// Custom error class to pass both title and message
export class APIError extends Error {
  title?: string;
  
  constructor(message: string, title?: string) {
    super(message);
    this.name = 'APIError';
    this.title = title;
  }
}

// Use production Render URL - environment variables should override if available
const API_BASE_URL = process.env.EXPO_PUBLIC_API_BASE_URL || 'https://agent-dashboard-backend.onrender.com';

// Retry configuration
const RETRY_CONFIG = {
  maxRetries: 3,
  initialDelay: 1000, // 1 second
  maxDelay: 8000, // 8 seconds
  backoffFactor: 2,
};

class DashboardAPI {
  private async getAuthHeaders() {
    const { data: { session } } = await supabase.auth.getSession();
    if (!session) throw new Error('No active session');
    
    return {
      'Authorization': `Bearer ${session.access_token}`,
      'Content-Type': 'application/json',
    };
  }

  private async sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }


  private async fetchWithAuth(url: string, options: RequestInit = {}, retryOptions?: { maxRetries?: number; priority?: 'high' | 'normal' }): Promise<any> {
    const maxRetries = retryOptions?.maxRetries ?? RETRY_CONFIG.maxRetries;
    
    console.log('[DashboardAPI] Making request to:', url, 'priority:', retryOptions?.priority || 'normal');
    
    for (let attempt = 0; attempt <= maxRetries; attempt++) {
      const controller = new AbortController();
      const timeoutMs = retryOptions?.priority === 'high' ? 15000 : 30000; // 15s for high priority, 30s default
      const timeoutId = setTimeout(() => controller.abort(), timeoutMs);
      
      try {
        const headers = await this.getAuthHeaders();
        console.log('[DashboardAPI] Request headers prepared for:', url);
        const response = await fetch(`${API_BASE_URL}${url}`, {
          ...options,
          headers: {
            ...headers,
            ...options.headers,
          },
          signal: controller.signal,
        });

        console.log('[DashboardAPI] Response received for:', url, 'status:', response.status);

        if (!response.ok) {
          const errorText = await response.text();
          let errorMessage = `HTTP error! status: ${response.status}`;
          let errorTitle = 'Error';
          
          try {
            const errorJson = JSON.parse(errorText);
            
            // Handle nested error in detail field
            if (errorJson.detail && typeof errorJson.detail === 'object') {
              errorTitle = errorJson.detail.error || 'Error';
              errorMessage = errorJson.detail.message || errorMessage;
            } else {
              errorTitle = errorJson.error || 'Error';
              errorMessage = errorJson.message || errorJson.detail || errorMessage;
            }
          } catch {
            errorMessage = errorText || errorMessage;
          }
          
          // Don't retry on auth errors (401, 403) or client errors (400-499)
          if (response.status >= 400 && response.status < 500) {
            throw new APIError(errorMessage, errorTitle);
          }

          // Retry on server errors (500+)
          reportError(new Error('API request failed'), {
            context: 'Server error response',
            extras: { url, status: response.status, errorMessage },
            tags: { feature: 'mobile-api' },
          });
          if (attempt < maxRetries) {
            console.warn(`[DashboardAPI] Retrying request (attempt ${attempt + 1}/${maxRetries + 1}): ${errorMessage}`);
            const delay = Math.min(
              RETRY_CONFIG.initialDelay * Math.pow(RETRY_CONFIG.backoffFactor, attempt),
              RETRY_CONFIG.maxDelay
            );
            await this.sleep(delay);
            continue;
          }
          
          // Also handle the error/message structure here for 500+ errors
          let finalErrorMessage = errorMessage;
          let finalErrorTitle = 'Error';
          
          try {
            const errorJson = JSON.parse(errorText);
            if (errorJson.detail && typeof errorJson.detail === 'object') {
              finalErrorTitle = errorJson.detail.error || 'Error';
              finalErrorMessage = errorJson.detail.message || errorMessage;
            } else if (errorJson.error || errorJson.message) {
              finalErrorTitle = errorJson.error || 'Error';
              finalErrorMessage = errorJson.message || errorJson.detail || errorMessage;
            }
          } catch {
            // Keep original errorMessage
          }
          
          throw new APIError(finalErrorMessage, finalErrorTitle);
        }

        clearTimeout(timeoutId);
        const data = await response.json();
        console.log('[DashboardAPI] Request successful:', url, 'data keys:', Object.keys(data));
        return data;
      } catch (error) {
        clearTimeout(timeoutId);
        reportError(error, {
          context: 'Network error during API request',
          extras: { url, attempt: attempt + 1 },
          tags: { feature: 'mobile-api' },
        });

        // Check if this is the last attempt
        if (attempt === maxRetries) {
          // Provide better error messages for common issues
          if (error instanceof Error) {
            if (error.name === 'AbortError') {
              throw new Error(`Request timed out after ${timeoutMs / 1000} seconds. Please check your connection and try again.`);
            }
            if (error.message === 'Network request failed') {
              throw new Error('Unable to connect to the server. Please ensure you have internet connectivity and the backend is running.');
            }
          }
          throw error;
        }

        // Calculate delay for exponential backoff
        const delay = Math.min(
          RETRY_CONFIG.initialDelay * Math.pow(RETRY_CONFIG.backoffFactor, attempt),
          RETRY_CONFIG.maxDelay
        );
        
        console.warn(`[DashboardAPI] Retrying after error (attempt ${attempt + 1}/${maxRetries + 1}): ${error instanceof Error ? error.message : 'Unknown error'}`);
        await this.sleep(delay);
      }
    }
  }

  // User Profile - marked as high priority for auth initialization
  async getCurrentUser(): Promise<UserProfile> {
    console.log('[DashboardAPI] getCurrentUser called');
    return this.fetchWithAuth('/api/v1/auth/me', {}, { priority: 'high' });
  }

  async updateUserProfile(updates: Partial<UserProfile>): Promise<UserProfile> {
    return this.fetchWithAuth('/api/v1/auth/me', {
      method: 'PATCH',
      body: JSON.stringify(updates),
    });
  }

  // Agent Types
  async getAgentTypes(): Promise<AgentType[]> {
    console.log('[DashboardAPI] getAgentTypes called');
    return this.fetchWithAuth('/api/v1/agent-types');
  }

  // Agent Instances
  async getAgentInstances(agentTypeId?: string): Promise<AgentInstance[]> {
    const url = agentTypeId 
      ? `/api/v1/agent-types/${agentTypeId}/instances`
      : '/api/v1/agent-instances';
    return this.fetchWithAuth(url);
  }

  async getInstanceDetail(instanceId: string, messageLimit?: number, beforeMessageId?: string): Promise<InstanceDetail> {
    const params = new URLSearchParams();
    if (messageLimit !== undefined) params.append('message_limit', messageLimit.toString());
    if (beforeMessageId) params.append('before_message_id', beforeMessageId);
    const queryString = params.toString();
    const url = `/api/v1/agent-instances/${instanceId}${queryString ? '?' + queryString : ''}`;
    return this.fetchWithAuth(url);
  }

  async getInstanceMessages(instanceId: string, limit: number = 50, beforeMessageId?: string): Promise<any[]> {
    const params = new URLSearchParams();
    params.append('limit', limit.toString());
    if (beforeMessageId) params.append('before_message_id', beforeMessageId);
    return this.fetchWithAuth(`/api/v1/agent-instances/${instanceId}/messages?${params.toString()}`);
  }

  // Messages - unified system
  async submitUserMessage(instanceId: string, content: string): Promise<void> {
    await this.fetchWithAuth(`/api/v1/agent-instances/${instanceId}/messages`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    });
  }

  // SSE Streaming
  async getMessageStreamUrl(instanceId: string): Promise<string | null> {
    const { data: { session } } = await supabase.auth.getSession();
    if (!session) {
      reportMessage('No auth session available for SSE', {
        context: 'Missing auth session for SSE stream',
        extras: { instanceId },
        tags: { feature: 'mobile-api' },
      });
      return null;
    }

    // Since EventSource doesn't support headers, we pass token as query param
    return `${API_BASE_URL}/api/v1/agent-instances/${instanceId}/messages/stream?token=${encodeURIComponent(session.access_token)}`;
  }


  // API Keys
  async getAPIKeys(): Promise<APIKey[]> {
    return this.fetchWithAuth('/api/v1/auth/api-keys');
  }

  async createAPIKey(name: string, expiresInDays?: number): Promise<NewAPIKey> {
    return this.fetchWithAuth('/api/v1/auth/api-keys', {
      method: 'POST',
      body: JSON.stringify({ name, expires_in_days: expiresInDays }),
    });
  }

  async revokeAPIKey(keyId: string): Promise<void> {
    await this.fetchWithAuth(`/api/v1/auth/api-keys/${keyId}`, {
      method: 'DELETE',
    });
  }

  // Agent Instance Actions
  async markInstanceComplete(instanceId: string): Promise<void> {
    await this.fetchWithAuth(`/api/v1/agent-instances/${instanceId}/status`, {
      method: 'PUT',
      body: JSON.stringify({ status: 'COMPLETED' }),
    });
  }

  async deleteInstance(instanceId: string): Promise<void> {
    await this.fetchWithAuth(`/api/v1/agent-instances/${instanceId}`, {
      method: 'DELETE',
    });
  }

  async updateAgentInstance(instanceId: string, data: { name: string }): Promise<AgentInstance> {
    return this.fetchWithAuth(`/api/v1/agent-instances/${instanceId}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  }

  // Push Notifications
  async registerPushToken(data: { token: string; platform: 'ios' | 'android' }): Promise<{ success: boolean; message: string }> {
    return this.fetchWithAuth('/api/v1/push/register', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  async deactivatePushToken(token: string): Promise<{ success: boolean; message: string }> {
    return this.fetchWithAuth(`/api/v1/push/deactivate/${encodeURIComponent(token)}`, {
      method: 'DELETE',
    });
  }

  async sendTestPushNotification(): Promise<{ success: boolean; message: string }> {
    return this.fetchWithAuth('/api/v1/push/send-test-push', {
      method: 'POST',
    });
  }

  // Webhook Types
  async getWebhookTypes(): Promise<any[]> {
    return this.fetchWithAuth('/api/v1/user-agents/webhook-types');
  }

  // User Agent Management
  async getUserAgents(): Promise<any[]> {
    return this.fetchWithAuth('/api/v1/user-agents');
  }

  async createUserAgent(data: {
    name: string;
    webhook_type?: string | null;
    webhook_config?: Record<string, any> | null;
    is_active: boolean;
  }): Promise<any> {
    console.log('API createUserAgent - sending data:', data);
    console.log('API createUserAgent - JSON payload:', JSON.stringify(data));
    
    try {
      const result = await this.fetchWithAuth('/api/v1/user-agents', {
        method: 'POST',
        body: JSON.stringify(data),
      });
      console.log('API createUserAgent - success:', result);
      return result;
    } catch (error) {
      reportError(error, {
        context: 'Failed to create user agent',
        extras: { data },
        tags: { feature: 'mobile-api' },
      });
      throw error;
    }
  }

  async updateUserAgent(id: string, data: {
    name: string;
    webhook_type?: string | null;
    webhook_config?: Record<string, any> | null;
    is_active: boolean;
  }): Promise<any> {
    return this.fetchWithAuth(`/api/v1/user-agents/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(data),
    });
  }

  async deleteUserAgent(id: string): Promise<void> {
    await this.fetchWithAuth(`/api/v1/user-agents/${id}`, {
      method: 'DELETE',
    });
  }

  async createAgentInstance(agentId: string, runtimeData: Record<string, any>): Promise<{
    success: boolean;
    agent_instance_id?: string;
    message: string;
    error?: string;
  }> {
    return this.fetchWithAuth(`/api/v1/user-agents/${agentId}/instances`, {
      method: 'POST',
      body: JSON.stringify(runtimeData),
    }, { maxRetries: 0 }); // No retries for webhook triggers
  }

  async getUserAgentInstances(agentId: string): Promise<AgentInstance[]> {
    return this.fetchWithAuth(`/api/v1/user-agents/${agentId}/instances`);
  }

  // Account Management
  async deleteAccount(): Promise<{ message: string; status_code?: number }> {
    return this.fetchWithAuth('/api/v1/auth/me', {
      method: 'DELETE',
    });
  }

  // Mobile Billing
  async getSubscriptionStatus(): Promise<{
    id: string;
    plan_type: string;
    agent_limit: number;
    current_period_end: string | null;
    cancel_at_period_end: boolean;
    provider: string | null;
  }> {
    return this.fetchWithAuth('/api/v1/billing/mobile/status');
  }

}

export const dashboardApi = new DashboardAPI();
