"""LLM utility functions for Omnara platform."""

import logging

import anthropic  # type: ignore

from shared.config import settings

logger = logging.getLogger(__name__)


def generate_conversation_title(user_message: str) -> str | None:
    """
    Generate a short title for a conversation based on the user's message.

    Args:
        user_message: The user's message content to summarize

    Returns:
        A 3-6 word title summarizing the conversation, or None if generation fails
    """
    try:
        # Check if we have an API key configured
        if not settings.anthropic_api_key:
            logger.debug("Anthropic API key not configured, skipping title generation")
            return None

        # Initialize Claude client
        client = anthropic.Anthropic(api_key=settings.anthropic_api_key)

        # Generate title using Claude
        prompt = f"""Generate a concise 3-5 word title for this conversation that just started with this message:

User message: "{user_message}"

Return ONLY the title, no quotes, no punctuation at the end, no explanation. The title should capture the essence of what the user is asking about or trying to do."""

        response = client.messages.create(
            model="claude-3-5-haiku-latest",
            max_tokens=20,
            temperature=0.3,
            messages=[{"role": "user", "content": prompt}],
        )

        # Extract the title from the response
        content = response.content[0]
        if isinstance(content, anthropic.types.TextBlock):
            title = content.text.strip()
        else:
            title = str(content)

        # Validate title length (should be reasonably short)
        if len(title) > 100:
            title = title[:97] + "..."

        logger.info(f"Generated title: {title}")
        return title

    except Exception as e:
        # Log the error but don't fail
        logger.error(f"Failed to generate title: {str(e)}")
        return None
