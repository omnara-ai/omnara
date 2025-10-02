"""Utilities for creating Supabase clients that can be reused across services."""

from __future__ import annotations

from functools import lru_cache

from supabase import Client, create_client

from shared.config.settings import settings


@lru_cache(maxsize=1)
def get_supabase_anon_client() -> Client:
    """Return a cached Supabase client using the anon key."""

    if not settings.supabase_url or not settings.supabase_anon_key:
        raise RuntimeError("Supabase anon credentials are not configured")

    return create_client(settings.supabase_url, settings.supabase_anon_key)


@lru_cache(maxsize=1)
def get_supabase_service_client() -> Client:
    """Return a cached Supabase client using the service role key."""

    if not settings.supabase_url or not settings.supabase_service_role_key:
        raise RuntimeError("Supabase service credentials are not configured")

    return create_client(settings.supabase_url, settings.supabase_service_role_key)
