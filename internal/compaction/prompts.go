package compaction

import "strings"

func SummaryPrompt(priorSummary, sourceText string) string {
	prior := "There is no earlier checkpoint."
	if strings.TrimSpace(priorSummary) != "" {
		prior = "Earlier cumulative checkpoint:\n" + priorSummary
	}
	return strings.Join([]string{
		"Produce a concise, complete cumulative continuation summary.",
		"The summary will be stored as an append-only checkpoint and supplied to a future model call as continuation state.",
		"Summarize state only. Do not continue the task, answer the user, call tools, expose hidden reasoning, or invent facts.",
		"Merge the earlier checkpoint with the newly closed events. Newer instructions and decisions supersede conflicting older ones; " +
			"remove stale state while preserving still-valid constraints and rationale.",
		"Preserve the current goal, durable user preferences, completed and active work, unresolved obligations and failures, " +
			"decisions, blockers, exact identifiers, files, resources, errors needed for continuity, and the next expected action. " +
			"Omit repetition, transient chatter, hidden reasoning, and tool-output detail that is no longer needed to continue correctly.",
		"Use this structure:",
		"## Goal\n[goal]",
		"## Instructions\n[durable instructions]",
		"## Progress\n[completed and in-progress work]",
		"## Relevant Artifacts\n[files, tool outputs, resources, or refs]",
		"## Next Steps\n[what should happen next]",
		prior,
		"Newly closed events:\n" + sourceText,
	}, "\n\n")
}
