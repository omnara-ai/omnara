package modelcontext

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type Builder struct {
	Store      Store
	Skills     SkillStore
	Normalizer Normalizer
}

func (b Builder) Build(ctx context.Context, input BuildInput) (Bundle, error) {
	if b.Store == nil {
		return Bundle{}, fmt.Errorf("modelcontext store is required")
	}
	if input.Now.IsZero() {
		return Bundle{}, fmt.Errorf("modelcontext build time is required")
	}
	var snapshot executionstore.AgentConfigSnapshotRecord
	if input.AgentConfigSnapshot != nil {
		snapshot = *input.AgentConfigSnapshot
	} else {
		captured, err := b.Store.CaptureAgentConfigForModelContext(ctx, input.ProjectID, input.AgentID)
		if err != nil {
			return Bundle{}, err
		}
		snapshot = captured
	}
	watermark := snapshot.InputEventSequence
	afterSequence := int64(0)
	var checkpointRef *CheckpointRef
	if input.CheckpointOverride != nil {
		if strings.TrimSpace(input.CheckpointOverride.Summary) == "" ||
			input.CheckpointOverride.SummarizedThroughEventSequence <= 0 ||
			input.CheckpointOverride.SummarizedThroughEventSequence > watermark {
			return Bundle{}, fmt.Errorf("valid checkpoint override within the model context watermark is required")
		}
		checkpointCopy := *input.CheckpointOverride
		if checkpointCopy.ID == "" {
			checkpointCopy.ID = "candidate"
		}
		checkpointRef = &checkpointCopy
		afterSequence = checkpointCopy.SummarizedThroughEventSequence
	} else {
		checkpoint, hasCheckpoint, err := b.Store.GetLatestApplicableContextCheckpoint(
			ctx,
			input.ProjectID,
			input.AgentID,
			watermark,
		)
		if err != nil {
			return Bundle{}, err
		}
		if hasCheckpoint {
			afterSequence = checkpoint.SummarizedThroughEventSequence
			checkpointRef = &CheckpointRef{
				ID:                             checkpoint.ID.String(),
				SummarizedThroughEventSequence: checkpoint.SummarizedThroughEventSequence,
				Summary:                        checkpoint.Summary,
			}
		}
	}
	messages, err := loadTranscriptWindow(
		ctx,
		b.Store,
		TranscriptWindowInput{
			ProjectID:     input.ProjectID,
			AgentID:       input.AgentID,
			Watermark:     watermark,
			AfterSequence: afterSequence,
		},
	)
	if err != nil {
		return Bundle{}, err
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		snapshot.AgentConfig.CompiledDefinition,
		snapshot.AgentConfig.CompilerVersion,
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
	if err != nil {
		return Bundle{}, err
	}
	integrationTargets, err := b.Store.ListIntegrationTargets(ctx, input.ProjectID, input.AgentID)
	if err != nil {
		return Bundle{}, err
	}
	contract, err = WithImplicitIntegrationMessageTool(contract, integrationTargets)
	if err != nil {
		return Bundle{}, err
	}
	toolSpecs, err := RuntimeContractToolSpecs(
		ctx,
		b.Store,
		input.ProjectID,
		input.AgentID,
		contract,
		input.Now,
	)
	if err != nil {
		return Bundle{}, err
	}
	var catalogSkills []skillstore.SkillRecord
	if HasTool(toolSpecs, toolcatalog.ToolNameSkill) {
		if b.Skills == nil {
			return Bundle{}, fmt.Errorf("modelcontext skill store is required")
		}
		catalogSkills, err = loadSkillCatalog(ctx, b.Skills, input.ProjectID, contract)
		if err != nil {
			return Bundle{}, err
		}
	}
	bundle := Bundle{
		ProjectID:          input.ProjectID,
		AgentID:            input.AgentID,
		TurnID:             input.TurnID,
		OpeningInputIDs:    input.OpeningInputIDs,
		InputEventSequence: watermark,
		SystemPrompt:       defaultSystemPromptForContract(contract, toolSpecs, catalogSkills),
	}
	bundle.ContextCheckpoint = checkpointRef
	if len(toolSpecs) > 0 {
		bundle.ToolSpecs = toolSpecs
	}
	if MachinePoolContextEnabled(toolSpecs) {
		sources, err := b.Store.ListMachinePoolSources(ctx, input.ProjectID, input.AgentID, snapshot.AgentConfig.ID)
		if err != nil {
			return Bundle{}, err
		}
		bundle.AvailableMachinePools = make([]MachinePoolRef, 0, len(sources))
		for _, source := range sources {
			bundle.AvailableMachinePools = append(bundle.AvailableMachinePools, MachinePoolRef{
				MachinePoolName: source.MachinePoolName,
				Description:     source.Description,
			})
		}
	}
	bundle.Messages = messages
	toolCalls, err := b.Store.ListCompletedToolCallsAtWatermark(
		ctx,
		input.ProjectID,
		input.AgentID,
		afterSequence,
		watermark,
	)
	if err != nil {
		return Bundle{}, err
	}
	var activeProcesses []executionstore.ActiveProcessRecord
	if ProcessContextEnabled(toolSpecs) {
		activeProcesses, err = b.Store.ListActiveProcessesForContext(
			ctx,
			input.ProjectID,
			input.AgentID,
		)
		if err != nil {
			return Bundle{}, err
		}
	}
	var executableMachineBindings []executionstore.AgentMachineBindingRecord
	if MachineContextEnabled(toolSpecs) {
		executableMachineBindings, err = b.Store.ListExecutableAgentMachineBindings(
			ctx,
			input.ProjectID,
			input.AgentID,
		)
		if err != nil {
			return Bundle{}, err
		}
	}
	bundle.ActiveProcesses = make([]ActiveProcessRef, 0, len(activeProcesses))
	for _, work := range activeProcesses {
		processID, err := publicid.Encode(publicid.KindProcess, work.ID)
		if err != nil {
			return Bundle{}, err
		}
		bundle.ActiveProcesses = append(
			bundle.ActiveProcesses,
			ActiveProcessRef{
				ProcessID:     processID,
				State:         string(work.State),
				CommandLabel:  processcmd.CommandLabel(work.Command),
				Command:       work.Command,
				ShellSelector: string(work.ShellSelector),
				Cwd:           work.Cwd,
			},
		)
	}
	bundle.AttachedMachines = make([]AttachedMachineRef, 0, len(executableMachineBindings))
	for _, binding := range executableMachineBindings {
		bundle.AttachedMachines = append(
			bundle.AttachedMachines,
			AttachedMachineRef{
				MachineRef:  binding.MachineRef,
				Description: binding.Description,
				Cwd:         binding.Cwd,
			},
		)
	}
	if IntegrationTargetContextEnabled(toolSpecs) {
		bundle.IntegrationTargets = make([]IntegrationTargetRef, 0, len(integrationTargets))
		for _, target := range integrationTargets {
			bundle.IntegrationTargets = append(bundle.IntegrationTargets, IntegrationTargetRef{
				TargetRef:       target.TargetRef,
				DurableID:       target.ID.String(),
				Provider:        target.Provider,
				ProviderRefKind: target.ProviderRefKind,
				Label:           integrationTargetLabel(target),
				InstallState:    string(target.InstallState),
				IsCurrent:       target.IsCurrent,
			})
		}
	}
	for _, toolCall := range toolCalls {
		parts := toolCall.ResultContentParts
		if len(parts) == 0 {
			parts = json.RawMessage(`[]`)
		}
		parts, err = modelToolResultContentParts(toolCall.Outcome, parts)
		if err != nil {
			return Bundle{}, fmt.Errorf("project tool result %s: %w", toolCall.ID, err)
		}
		bundle.ToolResults = append(bundle.ToolResults, ToolResultRef{
			ToolCallID:          toolCall.ID.String(),
			DurableID:           toolCall.ToolCallResultID.String(),
			EventID:             toolCall.ToolResultEventID.String(),
			SourceEventSequence: toolCall.SourceEventSequence,
			ResultEventSequence: toolCall.ToolResultEventSequence,
			ModelCallContextID:  toolCall.ModelCallContextID.String(),
			ProviderCallID:      toolCall.ProviderCallID,
			Name:                toolCall.Name,
			Input:               toolCall.Input,
			Outcome:             toolCall.Outcome,
			ContentParts:        parts,
		})
	}
	if err := b.resolveMedia(ctx, &bundle, input.MediaProjector); err != nil {
		return Bundle{}, err
	}
	if input.MediaProjector != nil {
		bundle.RenderedMedia = input.MediaProjector.ProjectRenderedMedia(bundle)
	}
	if err := normalizerOrDefault(b.Normalizer).Normalize(bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func integrationTargetLabel(target integrationstore.IntegrationTargetSummary) string {
	switch target.ProviderRefKind {
	case "dm":
		return target.Provider + " dm " + target.ProviderRef
	case "thread":
		if target.Provider == integrationstore.IntegrationProviderSlack {
			if label, ok := slackThreadLabel(target); ok {
				return label
			}
		}
		return target.Provider + " thread " + target.ProviderRef
	default:
		return target.Provider + " " + target.ProviderRefKind + " " + target.ProviderRef
	}
}

func slackThreadLabel(target integrationstore.IntegrationTargetSummary) (string, bool) {
	channelID, threadTS, ok := strings.Cut(target.ProviderRef, ":")
	if !ok || channelID == "" || threadTS == "" {
		return "", false
	}
	label := target.Provider + " thread " + threadTS + " in " + channelID
	if name := strings.TrimSpace(target.DisplayName); name != "" {
		label += " (#" + strings.TrimPrefix(name, "#") + ")"
	}
	return label, true
}

func HasTool(specs []ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func HasAnyTool(specs []ToolSpec, names ...string) bool {
	for _, name := range names {
		if HasTool(specs, name) {
			return true
		}
	}
	return false
}

func defaultSystemPromptForContract(
	contract agentconfig.RuntimeContract,
	toolSpecs []ToolSpec,
	skills []skillstore.SkillRecord,
) string {
	parts := []string{DefaultSystemPrompt()}
	if guidance := capabilityGuidance(toolSpecs); guidance != "" {
		parts = append(parts, guidance)
	}
	if instruction := strings.TrimSpace(contract.Instruction); instruction != "" {
		parts = append(parts, instruction)
	}
	if catalog := skillCatalogBlock(skills); catalog != "" {
		parts = append(parts, catalog)
	}
	return strings.Join(parts, "\n\n")
}

// loadSkillCatalog resolves the contract's attached skill ids to their latest
// revisions at model-call time; name and description are revision content, so
// they are intentionally not baked into the compiled contract. Skills deleted
// since compile are skipped rather than failing the build.
func loadSkillCatalog(
	ctx context.Context,
	store SkillStore,
	projectID storage.ID,
	contract agentconfig.RuntimeContract,
) ([]skillstore.SkillRecord, error) {
	if len(contract.Skills) == 0 {
		return nil, nil
	}
	records := make([]skillstore.SkillRecord, 0, len(contract.Skills))
	for _, skill := range contract.Skills {
		record, err := store.GetSkillForDispatch(ctx, projectID, skill.PublicID)
		if storeerr.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve skill %s for catalog: %w", skill.PublicID, err)
		}
		records = append(records, record)
	}
	return records, nil
}

func skillCatalogBlock(skills []skillstore.SkillRecord) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills are specialized instructions you can load on demand. ")
	b.WriteString("Call the `skill` tool with a skill's name to load its instructions. ")
	b.WriteString("When machines are attached, the skill's supporting files are installed ")
	b.WriteString("in its skill directory on each machine ")
	b.WriteString("at $OMNARA_HOME/installations/*/machines/*/skills/{skill_public_id}/")
	b.WriteString("revisions/{skill_revision_public_id}/. ")
	b.WriteString("Resolve any relative paths in a SKILL.md against that directory.\n\n")
	b.WriteString("<available_skills>\n")
	for _, skill := range skills {
		b.WriteString("  <skill>\n")
		b.WriteString("    <name>")
		writeXMLEscapedString(&b, skill.Name)
		b.WriteString("</name>\n    <description>")
		writeXMLEscapedString(&b, skill.Description)
		b.WriteString("</description>\n  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

func writeXMLEscapedString(b *strings.Builder, s string) {
	if err := xml.EscapeText(b, []byte(s)); err != nil {
		b.WriteString(s)
	}
}
