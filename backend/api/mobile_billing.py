"""Mobile billing endpoints for iOS and Android subscriptions."""

import json
import logging
from typing import Optional
from uuid import UUID
import httpx

from fastapi import APIRouter, Request, HTTPException, Depends
from sqlalchemy.orm import Session

from backend.auth.dependencies import get_current_user
from backend.models import SubscriptionResponse
from shared.config import settings
from shared.database.models import User
from shared.database.subscription_models import BillingEvent
from shared.database.session import get_db
from shared.database.billing_operations import (
    get_or_create_subscription,
    create_billing_event,
)

logger = logging.getLogger(__name__)

router = APIRouter(prefix="/billing/mobile", tags=["mobile-billing"])

# RevenueCat API configuration
REVENUECAT_API_URL = "https://api.revenuecat.com/v1"
REVENUECAT_API_KEY = settings.revenuecat_secret_key


@router.get("/status", response_model=SubscriptionResponse)
async def get_mobile_subscription_status(
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """
    Get current subscription status.

    Mobile apps should poll this endpoint after initiating a purchase
    to check when the webhook has processed the subscription.
    """
    subscription = get_or_create_subscription(current_user.id, db)

    return SubscriptionResponse(
        id=subscription.id,
        plan_type=subscription.plan_type,
        agent_limit=subscription.agent_limit,
        current_period_end=None,  # Mobile subscriptions managed by App Store/Play Store
        cancel_at_period_end=False,
    )


# Note: Cancellation is now handled entirely through webhooks.
# Users cancel in App Store/Play Store, and RevenueCat notifies us.


# ============== RevenueCat Webhook Handling ==============


async def fetch_subscriber_from_revenuecat(app_user_id: str) -> Optional[dict]:
    """Fetch current subscriber status from RevenueCat API."""
    if not REVENUECAT_API_KEY:
        logger.error("RevenueCat API key not configured")
        return None

    headers = {
        "Authorization": f"Bearer {REVENUECAT_API_KEY}",
        "Content-Type": "application/json",
    }

    async with httpx.AsyncClient() as client:
        try:
            response = await client.get(
                f"{REVENUECAT_API_URL}/subscribers/{app_user_id}",
                headers=headers,
            )
            response.raise_for_status()
            return response.json()
        except Exception as e:
            logger.error(f"Failed to fetch subscriber {app_user_id}: {e}")
            return None


def sync_subscription_status(subscriber_data: dict, db: Session) -> bool:
    """
    Sync RevenueCat subscriber data to our database.

    Since we're using Purchases.logIn() with user IDs, the app_user_id
    should match our user IDs directly.
    """
    if not subscriber_data:
        return False

    try:
        subscriber = subscriber_data.get("subscriber", {})
        app_user_id = subscriber.get("original_app_user_id")

        if not app_user_id:
            return False

        # Since we use logIn with user ID, app_user_id should be a valid UUID
        try:
            user_uuid = UUID(app_user_id)
        except ValueError:
            logger.error(f"Invalid user ID format from RevenueCat: {app_user_id}")
            return False

        # Get subscription directly by user ID
        subscription = get_or_create_subscription(user_uuid, db)

        # Update the customer ID to match (should already match due to logIn)
        if not subscription.provider_customer_id:
            subscription.provider_customer_id = app_user_id

        # Check if user has active Pro entitlement
        entitlements = subscriber.get("entitlements", {})
        has_pro = "pro" in entitlements

        # Also check active subscriptions
        subscriptions = subscriber.get("subscriptions", {})
        has_active_subscription = any(
            sub.get("is_active", False) for sub in subscriptions.values()
        )

        # Update subscription status
        if has_pro or has_active_subscription:
            # Determine provider from store
            store = subscriber.get("store", "app_store").lower()
            provider = "apple" if "app_store" in store else "google"

            subscription.plan_type = "pro"
            subscription.agent_limit = -1  # Unlimited
            subscription.provider = provider

            # Update provider subscription ID if available
            for sub_id, sub_data in subscriptions.items():
                if sub_data.get("is_active"):
                    subscription.provider_subscription_id = sub_id
                    break
        else:
            # No active subscription - revert to free
            subscription.plan_type = "free"
            subscription.agent_limit = 20
            # Keep provider info for potential restore

        db.commit()

        logger.info(
            f"Synced subscription for user {subscription.user_id}: "
            f"plan={subscription.plan_type}"
        )
        return True

    except Exception as e:
        logger.error(f"Failed to sync subscription status: {e}")
        db.rollback()
        return False


@router.post("/revenuecat/webhook")
async def handle_revenuecat_webhook(
    request: Request,
    db: Session = Depends(get_db),
):
    """
    Handle RevenueCat webhook events.

    Following RevenueCat's recommendation, we use webhooks as triggers
    to fetch the current subscription status rather than parsing each
    event type individually.
    """
    try:
        # Verify webhook authorization if configured
        if settings.revenuecat_webhook_auth_header:
            auth_header = request.headers.get("Authorization")
            if auth_header != f"Bearer {settings.revenuecat_webhook_auth_header}":
                logger.warning("Invalid webhook authorization header")
                raise HTTPException(status_code=401, detail="Unauthorized")
        # Get webhook payload
        body = await request.body()
        payload = json.loads(body)

        # Extract event details
        event = payload.get("event", {})
        event_type = event.get("type", "unknown")
        event_id = event.get("id")
        app_user_id = event.get("app_user_id")

        logger.info(
            f"Received RevenueCat webhook: {event_type} (id: {event_id}) for user {app_user_id}"
        )

        # Check if we've already processed this exact webhook event
        # Why we need this: While our subscription sync is idempotent (same input = same state),
        # we create billing event records for audit trails. Without deduplication:
        # - RevenueCat's 5 retries = 5 duplicate billing event records
        # - Unnecessary API calls to RevenueCat
        # - Potential rate limiting issues
        if event_id:
            existing_event = (
                db.query(BillingEvent)
                .filter(BillingEvent.provider_event_id == event_id)
                .first()
            )

            if existing_event:
                logger.info(
                    f"Already processed webhook event {event_id}, returning cached result"
                )
                # Still return 200 to acknowledge receipt
                return {"status": "already_processed", "event_id": event_id}

        # Skip if no user ID
        if not app_user_id:
            return {"status": "ignored", "reason": "no app_user_id"}

        # For certain events, we should process immediately
        important_events = [
            "INITIAL_PURCHASE",
            "RENEWAL",
            "CANCELLATION",
            "UNCANCELLATION",
            "EXPIRATION",
            "BILLING_ISSUE",
            "PRODUCT_CHANGE",  # User changed subscription tier
        ]

        # Only process important events
        if event_type not in important_events:
            logger.info(f"Ignoring non-critical event: {event_type}")
            return {"status": "ignored", "reason": "non-critical event"}

        # Fetch current subscriber status from RevenueCat
        subscriber_data = await fetch_subscriber_from_revenuecat(app_user_id)

        if not subscriber_data:
            logger.error(f"Failed to fetch subscriber data for {app_user_id}")
            return {"status": "error", "reason": "fetch failed"}

        # Sync to our database
        success = sync_subscription_status(subscriber_data, db)

        if not success:
            logger.error(f"Failed to sync subscription for user {app_user_id}")
            return {"status": "error", "reason": "sync failed"}

        # Log billing event for audit trail
        # Since we use user IDs directly, we can get the subscription by user ID
        try:
            user_uuid = UUID(app_user_id)
            subscription = get_or_create_subscription(user_uuid, db)
            if subscription:
                create_billing_event(
                    user_id=subscription.user_id,
                    subscription_id=subscription.id,
                    event_type=f"revenuecat_webhook_{event_type}",
                    event_data=json.dumps(payload),
                    provider_event_id=event_id,
                    db=db,
                )
        except ValueError:
            logger.error(f"Invalid user ID format in webhook: {app_user_id}")
            return {"status": "error", "reason": "invalid user id"}

        return {
            "status": "processed",
            "event_type": event_type,
        }

    except json.JSONDecodeError:
        logger.error("Invalid JSON in webhook payload")
        raise HTTPException(status_code=400, detail="Invalid JSON payload")
    except Exception as e:
        logger.error(f"Webhook processing error: {e}")
        # Return 200 to prevent RevenueCat from retrying
        return {"status": "error", "error": str(e)}
