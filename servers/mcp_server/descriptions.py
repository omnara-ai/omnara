"""Tool descriptions for MCP server"""

LOG_STEP_DESCRIPTION = """Log a high-level step the agent is performing.

⚠️  CRITICAL: MUST be called for EVERY significant action:
• Before answering any user question or request
• When performing analysis, searches, or investigations
• When reading files, exploring code, or gathering information
• When making code changes, edits, or file modifications
• When running commands, tests, or terminal operations
• When providing explanations, solutions, or recommendations
• At the start of multi-step processes or complex tasks

This call retrieves unread user feedback that you MUST incorporate into your work.
Feedback may contain corrections, clarifications, or additional instructions that override your original plan.

Args:
    agent_instance_id: Existing agent instance ID (optional). If omitted, creates a new instance for reuse in subsequent steps.
    step_description: Clear, specific description of what you're about to do or currently doing.

⚠️  RETURNS USER FEEDBACK: If user_feedback is not empty, you MUST:
    1. Read and understand each feedback message
    2. Adjust your current approach based on the feedback
    3. Acknowledge the feedback in your response
    4. Prioritize user feedback over your original plan

Feedback is automatically marked as retrieved. If empty, continue as planned."""


ASK_QUESTION_DESCRIPTION = """🤖 INTERACTIVE: Ask the user a question and WAIT for their reply (BLOCKS execution).

⚠️  CRITICAL: ALWAYS call log_step BEFORE using this tool to track the interaction.

🎯 USE WHEN YOU NEED:
• Clarification on ambiguous requirements or unclear instructions
• User decision between multiple valid approaches or solutions
• Confirmation before making significant changes (deleting files, major refactors)
• Missing information that you cannot determine from context or codebase
• User preferences for implementation details (styling, naming, architecture)
• Validation of assumptions before proceeding with complex tasks

💡 BEST PRACTICES:
• Keep questions clear, specific, and actionable
• Provide context: explain WHY you're asking
• Offer options when multiple choices exist
• Ask one focused question at a time
• Include relevant details to help user decide

Args:
    agent_instance_id: Current agent instance ID. REQUIRED.
    question_text: Clear, specific question with sufficient context for the user to provide a helpful answer."""


END_SESSION_DESCRIPTION = """End the current agent session and mark it as completed.

⚠️  IMPORTANT: Before using this tool, you MUST:
1. Provide a comprehensive summary of all actions taken to complete the task
2. Use the ask_question tool to confirm with the user that the task is complete
3. Only proceed with end_session if the user confirms completion

Example confirmation question:
"I've completed the following tasks:
• [List of specific actions taken]
• [Key changes or implementations made]
• [Any important outcomes or results]

Is this task complete and ready to be marked as finished?"

If the user:
• Confirms completion → Use end_session tool
• Does NOT confirm → Continue working on their feedback or new requirements
• Requests additional work → Do NOT end the session, continue with the new tasks

Use this tool ONLY when:
• The user has explicitly confirmed the task is complete
• The user explicitly asks to end the session
• An unrecoverable error prevents any further work

This will:
• Mark the agent instance status as COMPLETED
• Set the session end time
• Deactivate any pending questions
• Prevent further updates to this session

Args:
    agent_instance_id: Current agent instance ID to end. REQUIRED."""
