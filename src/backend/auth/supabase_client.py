import sys
from pathlib import Path

# Add parent directory to path to import shared module
sys.path.append(str(Path(__file__).parent.parent.parent))

from shared.config.settings import settings
from supabase import Client, create_client


def get_supabase_client() -> Client:
    """Get Supabase client with service role key for backend operations"""
    return create_client(settings.supabase_url, settings.supabase_service_role_key)


def get_supabase_anon_client() -> Client:
    """Get Supabase client with anon key (for testing purposes)"""
    return create_client(settings.supabase_url, settings.supabase_anon_key)
