package model

import "strings"

// reasoningEffortRank orders the effort values providers accept, lowest first.
var reasoningEffortRank = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// Effort is never lowered below "low": lower values disable reasoning outright
// on some providers and are incompatible with some of their tools.
var reasoningEffortFloor = reasoningEffortRank["low"]

var defaultReasoningEffortLadder = []string{"low", "medium", "high", "xhigh", "max"}

// LowerReasoningEffort steps effort down by up to steps levels among the
// supported values, never below "low". An unrecognized effort is returned
// unchanged.
func LowerReasoningEffort(effort string, supported []string, steps int) string {
	current, known := reasoningEffortRank[strings.ToLower(strings.TrimSpace(effort))]
	if !known {
		return effort
	}
	ladder := supported
	if len(ladder) == 0 {
		ladder = defaultReasoningEffortLadder
	}
	for range steps {
		next, nextRank := "", -1
		for _, candidate := range ladder {
			rank, ok := reasoningEffortRank[strings.ToLower(strings.TrimSpace(candidate))]
			if !ok || rank < reasoningEffortFloor || rank >= current || rank <= nextRank {
				continue
			}
			next, nextRank = candidate, rank
		}
		if next == "" {
			break
		}
		effort, current = next, nextRank
	}
	return effort
}

func claudeModelFamily(providerModelSlug string) string {
	if !IsClaudeFamilyModelSlug(providerModelSlug) {
		return ""
	}
	for _, segment := range strings.Split(normalizedProviderModelName(providerModelSlug), "-")[1:] {
		switch segment {
		case "opus", "sonnet", "haiku", "fable", "mythos":
			return segment
		}
	}
	return ""
}

// ClaudeSupportsPerMessageEffort reports whether the model accepts an effort
// change in a mid-conversation system message, per
// https://platform.claude.com/docs/en/build-with-claude/effort-per-message.
func ClaudeSupportsPerMessageEffort(providerModelSlug string) bool {
	major, minor, ok := claudeModelVersion(providerModelSlug)
	if !ok {
		return false
	}
	atLeast := func(wantMajor, wantMinor int) bool {
		return major > wantMajor || major == wantMajor && minor >= wantMinor
	}
	switch claudeModelFamily(providerModelSlug) {
	case "fable", "mythos":
		return atLeast(5, 1)
	case "opus":
		return atLeast(5, 0)
	case "sonnet", "haiku":
		return atLeast(5, 5)
	}
	return false
}

// ClaudeDefaultReasoningEffort is the effort a Claude model applies when a
// request omits output_config.effort, or "" when it is unknown.
func ClaudeDefaultReasoningEffort(providerModelSlug string) string {
	major, minor, ok := claudeModelVersion(providerModelSlug)
	if !ok || major < 5 {
		return ""
	}
	switch claudeModelFamily(providerModelSlug) {
	case "opus", "haiku":
		if major == 5 && minor == 5 {
			return "medium"
		}
	case "":
		return ""
	}
	return "high"
}
