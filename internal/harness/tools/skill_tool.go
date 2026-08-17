package tools

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// DefaultSkillSyncTimeout bounds Skill tool daemon installs.
const DefaultSkillSyncTimeout = 90 * time.Second

type skillToolInput struct {
	Name string `json:"name"`
}

func validateSkillInput(input json.RawMessage) error {
	_, err := resolveSkillToolRequest(input)
	return err
}

func resolveSkillToolRequest(raw json.RawMessage) (skillToolInput, error) {
	var input skillToolInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return skillToolInput{}, fmt.Errorf("parse skill request: %w", err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return skillToolInput{}, fmt.Errorf("parse skill request: %w", err)
	}
	for field, value := range body {
		if field != "name" {
			return skillToolInput{}, fmt.Errorf("skill request has unsupported field %q", field)
		}
		if string(value) == "null" {
			return skillToolInput{}, errors.New("skill name cannot be null")
		}
	}
	if err := skills.ValidateName(input.Name); err != nil {
		return skillToolInput{}, fmt.Errorf("skill name %w", err)
	}
	return input, nil
}

func runSkillTool(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	contract, err := call.Executor.runtimeContractForTurn(ctx, call.Turn)
	if err != nil {
		return nil, err
	}
	if len(contract.Skills) == 0 {
		return failSkillTool("no skills are attached to this agent")
	}
	skillStore := call.Executor.skillStore()
	if skillStore == nil {
		return nil, errors.New("skill store is required")
	}
	input, err := resolveSkillToolRequest(call.Call.Input)
	if err != nil {
		return failSkillTool(fmt.Sprintf("invalid skill input: %v", err))
	}
	// The contract pins only skill identities; names and descriptions live on
	// the latest revision, so resolve each attached skill now and match the
	// requested name against current revision content. Skills deleted since
	// compile simply drop out of the available set.
	var match *skillstore.SkillRecord
	available := make([]string, 0, len(contract.Skills))
	for _, attached := range contract.Skills {
		record, err := skillStore.GetSkillForDispatch(
			ctx,
			call.Turn.ProjectID,
			attached.PublicID,
		)
		if storeerr.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf(
				"resolve skill %s for dispatch: %w",
				attached.PublicID,
				err,
			)
		}
		available = append(available, record.Name)
		if match == nil && record.Name == input.Name {
			matched := record
			match = &matched
		}
	}
	if match == nil {
		return failSkillTool(fmt.Sprintf(
			"skill %q is not attached; available: %s",
			input.Name,
			strings.Join(available, ", "),
		))
	}
	matchPublicID, err := publicid.Encode(publicid.KindSkill, match.ID)
	if err != nil {
		return nil, fmt.Errorf("encode skill public id for %q: %w", match.Name, err)
	}
	record := *match
	if record.ArchiveDigest == "" {
		return nil, fmt.Errorf("skill %q has no archive digest", match.Name)
	}
	revisionPublicID, err := publicid.Encode(publicid.KindSkillRevision, record.RevisionID)
	if err != nil {
		return nil, fmt.Errorf(
			"encode skill revision id for %q: %w",
			match.Name,
			err,
		)
	}
	// A skill invocation always returns the SKILL.md instructions; installing
	// the archive on machines is best-effort. A skill can be pure markdown, so
	// having no attached machines (or failed installs) is not a tool failure —
	// the result reports per-machine install status and the model can retry
	// the skill tool to reattempt failed machines.
	bindings, err := call.Executor.Store.Execution().ListExecutableAgentMachineBindings(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
	)
	if err != nil {
		return nil, fmt.Errorf("list machine bindings for skill: %w", err)
	}
	var ready, failed []skills.BroadcastOutcome
	if len(bindings) > 0 {
		if call.Executor.SkillBroadcaster == nil {
			return failSkillTool("skill broadcaster is not configured on this worker")
		}
		targets := make([]skills.BroadcastTarget, 0, len(bindings))
		for _, b := range bindings {
			targets = append(targets, skills.BroadcastTarget{
				OrgID:      b.OrgID,
				MachineID:  b.MachineID,
				MachineRef: b.MachineRef,
			})
		}
		outcomes, err := call.Executor.SkillBroadcaster.BroadcastAndAwait(
			ctx,
			matchPublicID,
			revisionPublicID,
			record.ArchiveDigest,
			targets,
			DefaultSkillSyncTimeout,
		)
		if err != nil {
			return nil, fmt.Errorf("broadcast skill offer: %w", err)
		}
		for _, o := range outcomes {
			if o.IsReady() {
				ready = append(ready, o)
			} else {
				failed = append(failed, o)
			}
		}
	}

	_, body, ok := skills.SplitFrontmatter(record.SkillMd)
	if !ok || body == "" {
		body = record.SkillMd
	}
	wrapped := wrapSkillContent(match.Name, matchPublicID, revisionPublicID, ready, failed, body)
	content, err := skillToolSuccessResult(match.Name, wrapped)
	if err != nil {
		return nil, fmt.Errorf("marshal skill content: %w", err)
	}
	return completeAsynchronously(content), nil
}

func failSkillTool(message string) (asyncPhaseResult, error) {
	content, err := skillToolErrorResult(message)
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, errors.New(message)), nil
}

func skillToolErrorResult(message string) (toolResultContent, error) {
	return structuredToolResultContent(map[string]any{
		"error":   message,
		"message": message,
	})
}

func skillToolSuccessResult(name, content string) (toolResultContent, error) {
	return structuredToolResultContent(map[string]any{
		"name":    name,
		"content": content,
	})
}

func wrapSkillContent(
	name, publicID, revisionPublicID string,
	ready, failed []skills.BroadcastOutcome,
	body string,
) string {
	var b strings.Builder
	b.WriteString("<skill_content name=\"")
	writeXMLAttributeValue(&b, name)
	b.WriteString("\">\n")
	writeXMLText(&b, body)
	if len(ready) == 0 && len(failed) == 0 {
		b.WriteString("\n\nNo machines are attached; these instructions are available without an install.\n")
	}
	if len(ready) > 0 {
		b.WriteString("\n\nSkill directory on machine: ")
		writeXMLText(&b, SkillInstallPath(publicID, revisionPublicID))
		b.WriteString("\nResolve relative paths in this skill against that directory.\n")
		b.WriteString("\nInstalled on: ")
		writeXMLText(&b, joinReadyMachineRefs(ready))
		b.WriteString("\n")
	}
	if len(failed) > 0 {
		b.WriteString("Skill install failed on one or more machines.\n")
		b.WriteString("You may retry the skill tool to attempt installation again.\n")
	}
	b.WriteString("</skill_content>")
	return b.String()
}

func writeXMLAttributeValue(b *strings.Builder, s string) {
	writeXMLText(b, s)
}

func writeXMLText(b *strings.Builder, s string) {
	if err := xml.EscapeText(b, []byte(s)); err != nil {
		b.WriteString("[invalid text]")
	}
}

func joinReadyMachineRefs(outcomes []skills.BroadcastOutcome) string {
	refs := make([]string, 0, len(outcomes))
	for _, o := range outcomes {
		refs = append(refs, o.Target.MachineRef)
	}
	return strings.Join(refs, ", ")
}

// SkillInstallPath is the on-machine glob the daemon installs skills into.
func SkillInstallPath(skillPublicID, revisionPublicID string) string {
	if skillPublicID == "" || revisionPublicID == "" {
		return ""
	}
	return filepath.Join(
		"$OMNARA_HOME",
		localstore.InstallationsDirName,
		"*",
		localstore.MachinesDirName,
		"*",
		localstore.SkillsDirName,
		skillPublicID,
		localstore.SkillRevisionsDirName,
		revisionPublicID,
	)
}
