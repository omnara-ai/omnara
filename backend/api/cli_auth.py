"""CLI authentication endpoints"""

from fastapi import APIRouter, Query
from fastapi.responses import HTMLResponse, RedirectResponse
from urllib.parse import quote, urlencode
from shared.config import settings
import logging

logger = logging.getLogger(__name__)

router = APIRouter(tags=["cli-auth"])


@router.get("/cli-auth", response_class=HTMLResponse)
async def cli_auth_page(
    redirect_uri: str = Query(..., description="CLI callback URL"),
    state: str = Query(..., description="OAuth state parameter"),
):
    """
    Display CLI authentication page that redirects to Supabase OAuth.
    This page acts as an intermediary to handle the OAuth flow for CLI users.
    """

    # In production, this would redirect to your Supabase auth URL
    # For now, we'll create a simple HTML page that explains the flow
    html_content = f"""
    <!DOCTYPE html>
    <html>
    <head>
        <title>Omnara CLI Authentication</title>
        <style>
            body {{
                font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
                display: flex;
                justify-content: center;
                align-items: center;
                min-height: 100vh;
                margin: 0;
                background: #f5f5f5;
            }}
            .auth-container {{
                background: white;
                padding: 40px;
                border-radius: 12px;
                box-shadow: 0 4px 12px rgba(0,0,0,0.1);
                text-align: center;
                max-width: 400px;
            }}
            h1 {{
                color: #333;
                margin-bottom: 20px;
            }}
            p {{
                color: #666;
                line-height: 1.6;
                margin-bottom: 30px;
            }}
            .btn {{
                display: inline-block;
                background: #4F46E5;
                color: white;
                padding: 12px 30px;
                border-radius: 8px;
                text-decoration: none;
                font-weight: 500;
                transition: background 0.2s;
            }}
            .btn:hover {{
                background: #4338CA;
            }}
            .warning {{
                background: #FEF3C7;
                border: 1px solid #F59E0B;
                padding: 12px;
                border-radius: 6px;
                margin-top: 20px;
                font-size: 14px;
                color: #92400E;
            }}
        </style>
    </head>
    <body>
        <div class="auth-container">
            <h1>🔐 Omnara CLI Authentication</h1>
            <p>Click the button below to authenticate with your Omnara account. You'll be redirected to the secure login page.</p>

            <a href="/auth/cli/initiate?redirect_uri={quote(redirect_uri)}&state={quote(state)}" class="btn">
                Continue to Login
            </a>

            <div class="warning">
                ⚠️ Make sure you're on the official Omnara domain before entering credentials.
            </div>
        </div>
    </body>
    </html>
    """

    return html_content


@router.get("/auth/cli/initiate")
async def initiate_cli_auth(
    redirect_uri: str = Query(..., description="CLI callback URL"),
    state: str = Query(..., description="OAuth state parameter"),
):
    """
    Initiate the actual OAuth flow with Supabase.
    This redirects to Supabase's OAuth endpoint with appropriate parameters.
    """

    if not settings.supabase_url or not settings.supabase_anon_key:
        logger.warning("Supabase not configured, using dummy auth for development")
        # For development, simulate successful auth
        dummy_code = "dummy_auth_code_for_testing"
        return RedirectResponse(f"{redirect_uri}?code={dummy_code}&state={state}")

    # Build the Supabase OAuth URL
    base_url = (
        settings.frontend_urls[0]
        if settings.environment == "production"
        else "http://localhost:8000"
    )

    supabase_auth_url = f"{settings.supabase_url}/auth/v1/authorize"
    params = {
        "client_id": settings.supabase_anon_key,
        "redirect_uri": f"{base_url}/auth/cli/callback",
        "redirect_to": redirect_uri,  # Where to send user after Supabase auth
        "response_type": "code",
        "state": state,
        "provider": "github",  # Default to GitHub OAuth
    }

    return RedirectResponse(f"{supabase_auth_url}?{urlencode(params)}")


@router.get("/auth/cli/callback")
async def cli_auth_callback(
    code: str = Query(..., description="Authorization code from Supabase"),
    state: str = Query(..., description="OAuth state parameter"),
    redirect_to: str = Query(None, description="Final redirect URL"),
):
    """
    Handle the callback from Supabase OAuth.
    This endpoint is called by Supabase after successful authentication.
    """

    # Extract the original redirect URI from state or redirect_to
    if redirect_to:
        # Redirect to the CLI's local callback with the auth code
        final_url = f"{redirect_to}?code={code}&state={state}"
        return RedirectResponse(final_url)

    # Fallback to success page if no redirect_to provided
    return HTMLResponse("""
    <html>
    <head>
        <title>Authentication Successful</title>
        <style>
            body { font-family: Arial, sans-serif; text-align: center; padding: 50px; }
            .success { color: green; font-size: 24px; }
        </style>
        <script>
            // Try to extract redirect_to from state and redirect
            const urlParams = new URLSearchParams(window.location.search);
            const state = urlParams.get('state');
            const code = urlParams.get('code');

            // If we have localhost callback in referrer, redirect there
            if (document.referrer.includes('localhost:8765')) {
                window.location.href = `http://localhost:8765/callback?code=${code}&state=${state}`;
            }
        </script>
    </head>
    <body>
        <h1 class="success">✓ Authentication Successful!</h1>
        <p>You can close this window and return to your terminal.</p>
    </body>
    </html>
    """)
