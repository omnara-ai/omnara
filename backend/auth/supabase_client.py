"""Backend Supabase client - imports from shared."""

from shared.supabase import get_supabase_anon_client, get_supabase_client

# Re-export for backward compatibility
__all__ = ["get_supabase_client", "get_supabase_anon_client"]
