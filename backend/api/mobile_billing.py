"""Mobile billing endpoints for iOS and Android subscriptions."""

import json
import logging
from datetime import datetime, timezone
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
        current_period_end=None,
        cancel_at_period_end=False,
        provider=subscription.provider,
    )


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
        except httpx.HTTPStatusError as e:
            if e.response.status_code == 403:
                logger.error(
                    f"403 Forbidden when fetching subscriber {app_user_id}. "
                    f"Check that REVENUECAT_SECRET_KEY is set correctly and "
                    f"is for the same project as the webhook."
                )
            elif e.response.status_code == 404:
                logger.warning(f"Subscriber {app_user_id} not found in RevenueCat")
            else:
                logger.error(f"Failed to fetch subscriber {app_user_id}: {e}")
            return None
        except Exception as e:
            logger.error(f"Unexpected error fetching subscriber {app_user_id}: {e}")
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
        # Log the top-level keys to debug structure
        logger.info(
            f"Top-level RevenueCat response keys: {list(subscriber_data.keys())}"
        )

        subscriber = subscriber_data.get("subscriber", {})
        app_user_id = subscriber.get("original_app_user_id")

        logger.info(
            f"RevenueCat subscriber data keys: {list(subscriber.keys()) if subscriber else 'None'}"
        )
        logger.info(
            f"Entitlements type: {type(subscriber.get('entitlements'))}, value: {subscriber.get('entitlements')}"
        )

        if not app_user_id:
            return False

        try:
            user_uuid = UUID(app_user_id)
        except ValueError:
            logger.error(f"Invalid user ID format from RevenueCat: {app_user_id}")
            return False

        # Get subscription directly by user ID
        subscription = get_or_create_subscription(user_uuid, db)
        logger.info(
            f"Current subscription state: plan_type={subscription.plan_type}, user_id={subscription.user_id}"
        )

        # Check if user has the "Pro" entitlement
        # IMPORTANT: RevenueCat returns ALL entitlements including expired ones
        # We must check the expiration date to determine if it's currently active
        entitlements = subscriber.get("entitlements", {})
        has_pro_entitlement = False

        if "Pro" in entitlements:
            pro_entitlement = entitlements["Pro"]
            expires_date_str = pro_entitlement.get("expires_date")

            if expires_date_str:
                try:
                    # Parse the ISO format date from RevenueCat
                    # RevenueCat uses ISO 8601 with Z suffix for UTC
                    expires_date = datetime.fromisoformat(
                        expires_date_str.replace("Z", "+00:00")
                    )
                    current_time = datetime.now(timezone.utc)
                    has_pro_entitlement = current_time < expires_date

                    logger.info(
                        f"Pro entitlement expires at {expires_date.isoformat()}, "
                        f"current time {current_time.isoformat()}, active={has_pro_entitlement}"
                    )
                except ValueError as e:
                    logger.error(
                        f"Failed to parse expiration date '{expires_date_str}': {e}"
                    )
                    # If we can't parse the date, assume it's active to avoid accidental downgrades
                    has_pro_entitlement = True
            else:
                # No expiration date could mean lifetime subscription
                # But for our use case (monthly subscriptions), this shouldn't happen
                logger.warning("Pro entitlement found without expiration date")
                has_pro_entitlement = True

        logger.info(
            f"RevenueCat entitlements: {list(entitlements.keys()) if entitlements else 'None'}, has_pro={has_pro_entitlement}"
        )

        # Check for active subscriptions
        # RevenueCat returns all subscriptions, we need to find active ones
        subscriptions = subscriber.get("subscriptions", {})
        active_subscription_id = None
        active_store = None

        # Find subscription that hasn't expired and wasn't cancelled
        for sub_id, sub_data in subscriptions.items():
            expires_date = sub_data.get("expires_date")
            if expires_date and not sub_data.get("unsubscribe_detected_at"):
                # Simple check: if expires_date exists and no cancellation, consider it active
                # RevenueCat handles grace periods and other edge cases
                active_subscription_id = sub_data.get("store_transaction_id")
                active_store = sub_data.get("store", "app_store")
                break

        # Update subscription status based on the "pro" entitlement
        if has_pro_entitlement:
            # Determine provider from the active subscription's store
            if active_store:
                provider = "apple" if active_store == "app_store" else "google"
            else:
                # Fallback - check management URL
                management_url = subscriber.get("management_url")
                if management_url and "apple.com" in management_url:
                    provider = "apple"
                elif management_url and "play.google" in management_url:
                    provider = "google"
                else:
                    # Keep existing provider if we can't determine it
                    provider = subscription.provider

            subscription.plan_type = "pro"
            subscription.agent_limit = -1

            # Update customer ID if switching providers or if it's not set
            if (
                not subscription.provider_customer_id
                or subscription.provider != provider
            ):
                subscription.provider_customer_id = app_user_id

            subscription.provider = provider

            # Update provider subscription ID if available
            if active_subscription_id:
                subscription.provider_subscription_id = str(active_subscription_id)
        else:
            # No active subscription - revert to free
            logger.info("No active Pro entitlement found, downgrading to free")
            subscription.plan_type = "free"
            subscription.agent_limit = 10
            subscription.provider = None  # Clear provider when going to free
            subscription.provider_subscription_id = None  # Clear subscription reference

        db.commit()

        logger.info(
            f"Synced subscription for user {subscription.user_id}: "
            f"plan={subscription.plan_type}"
        )
        return True

    except Exception as e:
        logger.error(f"Failed to sync subscription status: {e}", exc_info=True)
        logger.error(f"Subscriber data: {subscriber_data}")
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
            if auth_header != settings.revenuecat_webhook_auth_header:
                logger.warning(
                    f"Invalid webhook authorization header. "
                    f"Received: '{auth_header}', Expected: '{settings.revenuecat_webhook_auth_header}'"
                )
                raise HTTPException(status_code=401, detail="Unauthorized")
        # Get webhook payload
        body = await request.body()
        payload = json.loads(body)

        # Extract event details from RevenueCat webhook structure
        event = payload.get("event", {})
        event_type = event.get("type", "unknown")
        event_id = event.get("id")
        app_user_id = event.get("app_user_id")
        original_app_user_id = event.get("original_app_user_id")

        logger.info(
            f"Received RevenueCat webhook: {event_type} (id: {event_id}) for user {app_user_id or original_app_user_id}"
        )

        # Check if we've already processed this webhook event
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
                return {"status": "already_processed", "event_id": event_id}

        # Use app_user_id (current) with fallback to original_app_user_id
        user_id_to_use = app_user_id or original_app_user_id
        if not user_id_to_use:
            return {"status": "ignored", "reason": "no app_user_id"}

        # For certain events, we should process immediately
        important_events = [
            "INITIAL_PURCHASE",
            "RENEWAL",
            "CANCELLATION",
            "UNCANCELLATION",
            "EXPIRATION",
            "BILLING_ISSUE",
            "PRODUCT_CHANGE",
            "TEST",  # RevenueCat test events
        ]

        # Only process important events
        if event_type not in important_events:
            logger.info(f"Ignoring non-critical event: {event_type}")
            return {"status": "ignored", "reason": "non-critical event"}

        # Fetch current subscriber status from RevenueCat
        subscriber_data = await fetch_subscriber_from_revenuecat(user_id_to_use)

        if not subscriber_data:
            logger.error(f"Failed to fetch subscriber data for {user_id_to_use}")
            return {"status": "error", "reason": "fetch failed"}

        # Sync to our database
        success = sync_subscription_status(subscriber_data, db)

        if not success:
            logger.error(f"Failed to sync subscription for user {user_id_to_use}")
            return {"status": "error", "reason": "sync failed"}

        # Log billing event for audit trail
        try:
            user_uuid = UUID(user_id_to_use)
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
            logger.error(f"Invalid user ID format in webhook: {user_id_to_use}")
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
        return {"status": "error", "error": str(e)}
