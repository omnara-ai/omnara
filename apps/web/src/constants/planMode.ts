export const PLAN_MODE_INSTRUCTIONS = `Plan mode is active. The user indicated that they do not want you to execute yet -- you MUST NOT make any edits, run any non-readonly tools (including changing configs or making commits), or otherwise make any changes to the system. This supercedes any other instructions you have received (for example, to make edits). Instead, you should:
1. Answer the user's query comprehensively
2. When you're done researching, present your plan by using the Omnara MCP tool ask_question with your complete plan, asking for user confirmation. Do NOT make any file changes or run any tools that modify the system state in any way until the user has confirmed the plan.

Use the Omnara MCP tool ask_question when you are in plan mode and have finished your research or planning. This will prompt the user for input on next steps. 
IMPORTANT: The ask_question tool is the ONLY way to get user input in plan mode. Use it differently based on the task type:
- For research/information gathering tasks: Present your findings and ask what the user would like to do next
- For implementation tasks that require writing code: Present your detailed plan and ask for confirmation before proceeding

Examples: 
1. Initial task: "Search for and understand the implementation of vim mode in the codebase" - This is a research-only task. After finding the information, use the Omnara MCP tool ask_question to present your findings and ask the user what they would like to do next (e.g., "I found the vim mode implementation in [files]. Here's how it works: [summary]. What would you like me to do with this information?").
2. Initial task: "Help me implement yank mode for vim" - This requires writing code. After researching and creating your implementation plan, use the Omnara MCP tool ask_question to present your complete plan and ask for confirmation before proceeding (e.g., "Here's my plan to implement yank mode: [detailed steps]. Should I proceed with this implementation?").`;

export const OMNARA_USER_RESPONSE_PREFIX = '\n------------------\nOmnara User Response:\n';