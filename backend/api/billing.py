"""Billing and subscription management endpoints."""

import json
import logging
from datetime import datetime, timedelta
from uuid import UUID

import stripe
from fastapi import APIRouter, Depends, HTTPException, Request
from sqlalchemy.orm import Session
from stripe import StripeError, SignatureVerificationError

from backend.auth.dependencies import get_current_user
from backend.models import (
    CreateCheckoutSessionRequest,
    CheckoutSessionResponse,
    PromoCodeValidationResponse,
    SubscriptionResponse,
    UsageResponse,
    ValidatePromoCodeRequest,
)
from shared.config import settings
from shared.database.billing_operations import (
    count_monthly_agents,
    create_billing_event,
    find_subscription_by_customer_id,
    find_subscription_by_provider_id,
    get_or_create_subscription,
    update_subscription_customer_id,
)
from shared.database.models import User
from shared.database.session import get_db

logger = logging.getLogger(__name__)

# Configure Stripe - this module is only loaded when STRIPE_SECRET_KEY is configured
stripe.api_key = settings.stripe_secret_key

router = APIRouter(prefix="/billing", tags=["billing"])


# API Endpoints
@router.get("/subscription", response_model=SubscriptionResponse)
async def get_subscription(
    current_user: User = Depends(get_current_user), db: Session = Depends(get_db)
):
    """Get current user's subscription details."""
    subscription = get_or_create_subscription(current_user.id, db)

    response = SubscriptionResponse(
        id=subscription.id,
        plan_type=subscription.plan_type,
        agent_limit=subscription.agent_limit,
        current_period_end=None,
        cancel_at_period_end=False,
        provider=subscription.provider,
    )

    # Fetch additional details from Stripe if available
    if subscription.provider_subscription_id:
        try:
            stripe_sub = stripe.Subscription.retrieve(
                subscription.provider_subscription_id
            )
            response.current_period_end = datetime.fromtimestamp(
                stripe_sub.current_period_end
            )
            response.cancel_at_period_end = stripe_sub.cancel_at_period_end
        except Exception as e:
            logger.error(f"Failed to fetch Stripe subscription details: {e}")

    return response


@router.get("/usage", response_model=UsageResponse)
async def get_usage(
    current_user: User = Depends(get_current_user), db: Session = Depends(get_db)
):
    """Get current usage statistics."""
    from datetime import date

    subscription = get_or_create_subscription(current_user.id, db)

    # Calculate current month period
    today = date.today()
    period_start = today.replace(day=1)

    # Calculate period end (last day of current month)
    if today.month == 12:
        period_end = date(today.year + 1, 1, 1) - timedelta(days=1)
    else:
        period_end = date(today.year, today.month + 1, 1) - timedelta(days=1)

    # Count total agents created this month
    monthly_agents = count_monthly_agents(current_user.id, db)

    return UsageResponse(
        total_agents=monthly_agents,
        agent_limit=subscription.agent_limit,
        period_start=datetime.combine(period_start, datetime.min.time()),
        period_end=datetime.combine(period_end, datetime.max.time()),
    )


@router.post("/checkout", response_model=CheckoutSessionResponse)
async def create_checkout_session(
    request: CreateCheckoutSessionRequest,
    current_user: User = Depends(get_current_user),
    db: Session = Depends(get_db),
):
    """Create a Stripe checkout session for upgrading subscription."""

    # Map plan types to Stripe price IDs
    price_mapping = {
        "pro": settings.stripe_pro_price_id,
        "enterprise": settings.stripe_enterprise_price_id,
    }

    price_id = price_mapping.get(request.plan_type)
    if not price_id:
        raise HTTPException(
            status_code=400, detail=f"Invalid plan type: {request.plan_type}"
        )

    # Get or create Stripe customer
    subscription = get_or_create_subscription(current_user.id, db)

    # Check if we need to create a new Stripe customer
    # This handles cases where user previously had mobile subscription
    if not subscription.provider_customer_id or subscription.provider != "stripe":
        # Create Stripe customer
        customer = stripe.Customer.create(
            email=current_user.email, metadata={"user_id": str(current_user.id)}
        )
        update_subscription_customer_id(
            subscription, customer.id, db, provider="stripe"
        )

    # Build checkout session parameters
    checkout_params = {
        "customer": subscription.provider_customer_id,
        "payment_method_types": ["card"],
        "line_items": [
            {
                "price": price_id,
                "quantity": 1,
            }
        ],
        "mode": "subscription",
        "success_url": request.success_url,
        "cancel_url": request.cancel_url,
        "client_reference_id": str(current_user.id),
    }

    # If a specific promo code is provided, validate and apply it
    if request.promo_code:
        try:
            # Look up the promotion code
            promo_codes = stripe.PromotionCode.list(
                code=request.promo_code, active=True
            )
            if promo_codes.data:
                promo_code = promo_codes.data[0]
                # Apply the specific discount (cannot use allow_promotion_codes with discounts)
                checkout_params["discounts"] = [{"promotion_code": promo_code.id}]
            else:
                # Promo code not found, allow user to enter codes in checkout
                checkout_params["allow_promotion_codes"] = True
                logger.warning(f"Promo code not found: {request.promo_code}")
        except Exception as e:
            # If error occurs, still allow promo codes in checkout
            logger.warning(f"Error validating promo code: {request.promo_code} - {e}")
            checkout_params["allow_promotion_codes"] = True
    else:
        # No promo code provided, allow users to enter one in checkout
        checkout_params["allow_promotion_codes"] = True

    # Create checkout session
    checkout_session = stripe.checkout.Session.create(**checkout_params)

    # Log billing event
    create_billing_event(
        user_id=current_user.id,
        subscription_id=subscription.id,
        event_type="checkout_session_created",
        event_data=json.dumps(
            {"session_id": checkout_session.id, "plan_type": request.plan_type}
        ),
        provider_event_id=checkout_session.id,
        db=db,
    )

    if not checkout_session.url:
        raise HTTPException(
            status_code=500, detail="Failed to create checkout session URL"
        )

    return CheckoutSessionResponse(
        checkout_url=checkout_session.url, session_id=checkout_session.id
    )


@router.post("/validate-promo", response_model=PromoCodeValidationResponse)
async def validate_promo_code(
    request: ValidatePromoCodeRequest,
    current_user: User = Depends(get_current_user),
):
    """Validate a promotional code before checkout."""

    try:
        # Look up the promotion code
        promo_codes = stripe.PromotionCode.list(code=request.code, active=True, limit=1)

        if not promo_codes.data:
            return PromoCodeValidationResponse(
                valid=False, error="Invalid or expired promotional code"
            )

        promo_code = promo_codes.data[0]
        coupon = promo_code.coupon

        # Check if coupon is valid
        if not coupon.valid:
            return PromoCodeValidationResponse(
                valid=False, error="This promotional code is no longer valid"
            )

        # Get discount details
        discount_type = "percentage" if coupon.percent_off else "amount"
        discount_value = (
            coupon.percent_off
            if coupon.percent_off
            else (coupon.amount_off / 100 if coupon.amount_off else 0)
        )  # Convert cents to dollars

        # Build description
        if discount_type == "percentage":
            description = f"{int(discount_value)}% off"
        else:
            description = f"${discount_value:.2f} off"

        if coupon.duration == "once":
            description += " (first payment only)"
        elif coupon.duration == "repeating":
            description += f" for {coupon.duration_in_months} months"
        elif coupon.duration == "forever":
            description += " forever"

        return PromoCodeValidationResponse(
            valid=True,
            code=request.code,
            discount_type=discount_type,
            discount_value=discount_value,
            description=description,
        )

    except Exception as e:
        logger.error(f"Error validating promo code: {e}")
        return PromoCodeValidationResponse(
            valid=False, error="Failed to validate promotional code"
        )


@router.post("/cancel")
async def cancel_subscription(
    current_user: User = Depends(get_current_user), db: Session = Depends(get_db)
):
    """Cancel the current subscription at period end."""
    subscription = get_or_create_subscription(current_user.id, db)

    if subscription.provider_subscription_id:
        # Cancel in Stripe at period end (user keeps access until then)
        try:
            stripe.Subscription.modify(
                subscription.provider_subscription_id, cancel_at_period_end=True
            )
            # Note: User keeps their current plan until period ends
            # When period ends, Stripe sends customer.subscription.deleted webhook
            # That webhook will reset them to free tier (see handle_subscription_deleted)
        except StripeError as e:
            logger.error(f"Failed to cancel subscription: {e}")
            raise HTTPException(status_code=500, detail="Failed to cancel subscription")

    db.commit()

    return {
        "message": "Subscription will be cancelled at the end of the billing period"
    }


@router.post("/webhook")
async def stripe_webhook(request: Request, db: Session = Depends(get_db)):
    """Handle Stripe webhook events."""
    if not settings.stripe_webhook_secret:
        raise HTTPException(status_code=503, detail="Webhooks not configured")

    payload = await request.body()
    sig_header = request.headers.get("stripe-signature")

    try:
        event = stripe.Webhook.construct_event(
            payload, sig_header, settings.stripe_webhook_secret
        )
    except ValueError:
        raise HTTPException(status_code=400, detail="Invalid payload")
    except SignatureVerificationError:
        raise HTTPException(status_code=400, detail="Invalid signature")

    # TODO: Consider refactoring to return 200 immediately and process asynchronously
    # Stripe docs recommend: "Your endpoint must quickly return a successful status code (2xx)
    # prior to any complex logic that could cause a timeout."

    event_object = event["data"]["object"]

    try:
        user_id = None
        subscription_id = None
        customer_id = event_object.get("customer")

        # Try to find user by customer_id first
        if customer_id:
            sub_with_customer = find_subscription_by_customer_id(customer_id, db)
            if sub_with_customer:
                user_id = sub_with_customer.user_id
                subscription_id = sub_with_customer.id

        # Handle specific event types
        if event["type"] == "checkout.session.completed":
            client_ref_id = event_object.get("client_reference_id")
            if client_ref_id:
                user_id = UUID(client_ref_id)

        elif event["type"] in [
            "customer.subscription.updated",
            "customer.subscription.deleted",
        ]:
            # Try to find subscription by Stripe ID if we didn't find it by customer
            if not user_id:
                existing_sub = find_subscription_by_provider_id(event_object["id"], db)
                if existing_sub:
                    user_id = existing_sub.user_id
                    subscription_id = existing_sub.id

        # Only log events if we can associate them with a user
        if user_id:
            create_billing_event(
                user_id=user_id,
                subscription_id=subscription_id,
                event_type=event["type"],
                event_data=json.dumps(event_object),
                provider_event_id=event["id"],
                db=db,
            )
        else:
            logger.debug(
                f"Skipping event logging for {event['type']} - no user association"
            )
    except Exception as e:
        logger.error(f"Failed to log webhook event {event['id']}: {e}")
        # Don't fail the webhook - we still want to process it

    # Process specific event types
    try:
        if event["type"] == "checkout.session.completed":
            handle_checkout_completed(event_object, db)

        elif event["type"] == "customer.subscription.updated":
            handle_subscription_updated(event_object, db)

        elif event["type"] == "customer.subscription.deleted":
            handle_subscription_deleted(event_object, db)

        # Add more event handlers as needed
        else:
            logger.info(f"Unhandled webhook event type: {event['type']}")

    except Exception as e:
        logger.error(f"Error processing webhook event {event['id']}: {e}")
        # Return 200 anyway to prevent Stripe from retrying
        # We've already logged the event, so we can investigate later

    return {"received": True}


def handle_checkout_completed(session: dict, db: Session):
    """Handle successful checkout session completion."""
    if not session.get("client_reference_id"):
        logger.warning(f"Checkout session {session['id']} has no client_reference_id")
        return

    user_id = UUID(session["client_reference_id"])
    subscription = get_or_create_subscription(user_id, db)

    # Update subscription with Stripe IDs
    # Only update customer ID if switching providers or if it's not set
    customer_id = session.get("customer")
    if customer_id and (
        not subscription.provider_customer_id or subscription.provider != "stripe"
    ):
        subscription.provider_customer_id = customer_id
    subscription.provider_subscription_id = session.get("subscription")

    # Fetch the full session with line items to determine plan type
    if session.get("id"):
        try:
            # Retrieve the session with expanded line items
            full_session = stripe.checkout.Session.retrieve(
                session["id"], expand=["line_items"]
            )

            # Get the price ID from line items
            if (
                full_session.line_items
                and full_session.line_items.data
                and len(full_session.line_items.data) > 0
                and full_session.line_items.data[0].price
            ):
                price_id = full_session.line_items.data[0].price.id
                logger.info(f"Checkout price_id: {price_id}")
                logger.info(f"Configured pro price_id: {settings.stripe_pro_price_id}")
                logger.info(
                    f"Configured enterprise price_id: {settings.stripe_enterprise_price_id}"
                )

                # Map price ID to plan type
                if price_id == settings.stripe_pro_price_id:
                    subscription.plan_type = "pro"
                    subscription.agent_limit = -1  # Unlimited
                    subscription.provider = "stripe"
                elif price_id == settings.stripe_enterprise_price_id:
                    subscription.plan_type = "enterprise"
                    subscription.agent_limit = -1  # Unlimited
                    subscription.provider = "stripe"
                else:
                    logger.warning(
                        f"Unknown price ID in checkout: {price_id}, not matching pro: {settings.stripe_pro_price_id} or enterprise: {settings.stripe_enterprise_price_id}"
                    )
        except Exception as e:
            logger.error(f"Failed to fetch checkout session line items: {e}")

    db.commit()
    logger.info(
        f"Activated subscription for user {user_id} with plan {subscription.plan_type}"
    )


def handle_subscription_updated(stripe_sub: dict, db: Session):
    """Handle subscription updates (status changes, plan changes, etc)."""
    subscription = find_subscription_by_provider_id(stripe_sub["id"], db)

    if not subscription:
        logger.warning(f"Received update for unknown subscription {stripe_sub['id']}")
        return

    # Check if plan has changed by examining the price ID
    if (
        stripe_sub.get("items")
        and stripe_sub["items"].get("data")
        and len(stripe_sub["items"]["data"]) > 0
        and stripe_sub["items"]["data"][0].get("price")
        and stripe_sub["items"]["data"][0]["price"].get("id")
    ):
        price_id = stripe_sub["items"]["data"][0]["price"]["id"]

        # Map price ID to plan type
        if price_id == settings.stripe_pro_price_id:
            subscription.plan_type = "pro"
            subscription.agent_limit = -1  # Unlimited
            subscription.provider = "stripe"
        elif price_id == settings.stripe_enterprise_price_id:
            subscription.plan_type = "enterprise"
            subscription.agent_limit = -1  # Unlimited
            subscription.provider = "stripe"

    db.commit()
    logger.info(f"Updated subscription {subscription.id}")


def handle_subscription_deleted(stripe_sub: dict, db: Session):
    """Handle subscription deletion/cancellation - reset to free tier."""
    subscription = find_subscription_by_provider_id(stripe_sub["id"], db)

    if not subscription:
        logger.warning(f"Received deletion for unknown subscription {stripe_sub['id']}")
        return

    # Reset to free tier
    subscription.plan_type = "free"
    subscription.agent_limit = settings.free_plan_agent_limit
    subscription.provider_subscription_id = None  # Clear the Stripe reference
    subscription.provider = None  # Clear provider when going to free

    db.commit()
    logger.info(f"Reset subscription {subscription.id} to free tier after cancellation")
