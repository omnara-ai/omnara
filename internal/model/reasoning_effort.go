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

// EffortSteering tracks the effort in force while a provider renders history
// with per-message effort changes, which stay in effect until the next one.
type EffortSteering struct {
	current   string
	supported []string
}

// NewEffortSteering starts from the request's effective effort. It returns
// nil when that effort is unknown, since there is nothing to lower from.
func NewEffortSteering(effort string, supported []string) *EffortSteering {
	if _, known := reasoningEffortRank[strings.ToLower(strings.TrimSpace(effort))]; !known {
		return nil
	}
	return &EffortSteering{current: effort, supported: supported}
}

// Lower drops the effort in force by steps levels and returns the new level,
// or false when it cannot go lower and no change should be rendered.
func (s *EffortSteering) Lower(steps int) (string, bool) {
	if s == nil || steps <= 0 {
		return "", false
	}
	lowered := LowerReasoningEffort(s.current, s.supported, steps)
	if strings.EqualFold(lowered, s.current) {
		return "", false
	}
	s.current = lowered
	return lowered, true
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

var openRouterConfigurationUpdateGPTModels = []string{
	"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-6.1-sol", "gpt-6.1-terra",
}

// OpenRouterSupportsConfigurationUpdate reports whether OpenRouter accepts a
// mid-conversation configuration_update for the model, per
// https://openrouter.ai/docs/use-cases/reasoning-tokens. Other models get a 400.
func OpenRouterSupportsConfigurationUpdate(providerModelSlug string) bool {
	name := normalizedProviderModelName(providerModelSlug)
	for _, gpt := range openRouterConfigurationUpdateGPTModels {
		// Dated snapshots share the update support; pro variants do not.
		if name == gpt || strings.HasPrefix(name, gpt+"-2") {
			return true
		}
	}
	major, minor, ok := claudeModelVersion(providerModelSlug)
	if !ok || major != 5 {
		return false
	}
	switch claudeModelFamily(providerModelSlug) {
	case "fable":
		return minor == 1 || minor == 2
	case "opus":
		return minor == 0
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
