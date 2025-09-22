import { supabase } from '../supabase';
import { authClient } from '../auth/authClient';
import { toast } from '@/hooks/use-toast';

const API_BASE_URL = import.meta.env.VITE_API_URL || 'https://agent-dashboard-backend.onrender.com';

export interface Subscription {
  id: string;
  plan_type: string;
  agent_limit: number;
  current_period_end?: string | null;
  cancel_at_period_end?: boolean;
}

export interface Usage {
  total_agents: number;
  agent_limit: number;
  period_start: string;
  period_end: string;
}

export interface CheckoutSessionResponse {
  checkout_url: string;
  session_id: string;
}

class BillingApiClient {
  private async getAuthToken(): Promise<string | null> {
    const { data: { session } } = await supabase.auth.getSession();
    return session?.access_token || null;
  }

  private async request<T>(endpoint: string, options?: RequestInit): Promise<T> {
    try {
      const token = await this.getAuthToken();
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
      };
      
      if (options?.headers) {
        Object.assign(headers, options.headers);
      }
      
      if (token) {
        headers['Authorization'] = `Bearer ${token}`;
      }

      const response = await fetch(`${API_BASE_URL}${endpoint}`, {
        headers,
        // Do not send cross-site cookies; rely on Bearer token only
        ...options,
      });

      if (!response.ok) {
        if (response.status === 401) {
          // Clear local auth first to prevent stale-token loops
          await authClient.signOut().catch(() => {})
          // Don't automatically redirect for promo code validation
          if (!endpoint.includes('/validate-promo')) {
            toast({
              title: "Authentication Required",
              description: "Please sign in to access the dashboard.",
              variant: "destructive",
            });
            window.location.href = '/';
          }
          throw new Error('Authentication required');
        }
        
        const errorData = await response.json().catch(() => ({}));
        throw new Error(errorData.detail || `Request failed with status ${response.status}`);
      }

      return response.json();
    } catch (error) {
      console.error('API request failed:', error);
      throw error;
    }
  }

  async getSubscription(): Promise<Subscription> {
    return this.request<Subscription>('/api/v1/billing/subscription');
  }

  async getUsage(): Promise<Usage> {
    return this.request<Usage>('/api/v1/billing/usage');
  }

  async createCheckoutSession(planType: string, successUrl: string, cancelUrl: string, promoCode?: string): Promise<CheckoutSessionResponse> {
    return this.request<CheckoutSessionResponse>('/api/v1/billing/checkout', {
      method: 'POST',
      body: JSON.stringify({
        plan_type: planType,
        success_url: successUrl,
        cancel_url: cancelUrl,
        promo_code: promoCode,
      }),
    });
  }

  async validatePromoCode(code: string, planType: string): Promise<{
    valid: boolean;
    code?: string;
    discount_type?: string;
    discount_value?: number;
    description?: string;
    error?: string;
  }> {
    return this.request('/api/v1/billing/validate-promo', {
      method: 'POST',
      body: JSON.stringify({
        code,
        plan_type: planType,
      }),
    });
  }

  async cancelSubscription(): Promise<void> {
    await this.request('/api/v1/billing/cancel', {
      method: 'POST',
    });
  }
}

export const billingApi = new BillingApiClient();
