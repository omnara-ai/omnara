package model

import "strings"

var reasoningEffortRank = map[string]int{
	"none":    0,
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
}

// Lower values disable reasoning on some providers.
var reasoningEffortFloor = reasoningEffortRank["low"]

var defaultReasoningEffortLadder = []string{"low", "medium", "high", "xhigh", "max"}

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

type EffortSteering struct {
	base      string
	current   string
	supported []string
}

func NewEffortSteering(effort string, supported []string) EffortSteering {
	if _, known := reasoningEffortRank[strings.ToLower(strings.TrimSpace(effort))]; !known {
		return EffortSteering{}
	}
	return EffortSteering{base: effort, current: effort, supported: supported}
}

// Restore returns the configured effort when a lowered one is in force.
func (s *EffortSteering) Restore() (string, bool) {
	if s == nil || s.current == s.base {
		return "", false
	}
	s.current = s.base
	return s.base, true
}

func (s *EffortSteering) Lower(steps int) (string, bool) {
	if s == nil || s.current == "" || steps <= 0 {
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

// ClaudeSupportsPerMessageEffort follows https://platform.claude.com/docs/en/build-with-claude/effort-per-message.
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

// OpenRouterSupportsConfigurationUpdate follows https://openrouter.ai/docs/use-cases/reasoning-tokens.
func OpenRouterSupportsConfigurationUpdate(providerModelSlug string) bool {
	name := normalizedProviderModelName(providerModelSlug)
	for _, gpt := range openRouterConfigurationUpdateGPTModels {
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
