package modelcontext

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	agentPublicID, err := publicid.Encode(publicid.KindAgent, input.AgentID)
	if err != nil {
		return Bundle{}, fmt.Errorf("encode agent public id: %w", err)
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
	if checkpointRef != nil {
		checkpointRef.EndsWithOutputLimit, err = b.Store.IsOutputLimitBoundary(
			ctx, input.ProjectID, input.AgentID, checkpointRef.SummarizedThroughEventSequence,
		)
		if err != nil {
			return Bundle{}, fmt.Errorf("load checkpoint output boundary: %w", err)
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
		snapshot.AgentConfig.EffectiveDefinitionHash,
	)
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
		SystemPrompt:       defaultSystemPromptForContract(agentPublicID, contract, toolSpecs, catalogSkills),
	}
	bundle.ContextCheckpoint = checkpointRef
	if len(toolSpecs) > 0 {
		bundle.ToolSpecs = toolSpecs
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

func HasTool(specs []ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func defaultSystemPromptForContract(
	agentPublicID string,
	contract agentconfig.RuntimeContract,
	toolSpecs []ToolSpec,
	skills []skillstore.SkillRecord,
) string {
	parts := []string{DefaultSystemPrompt(), "Your Omnara agent ID is `" + agentPublicID + "`."}
	if guidance := capabilityGuidance(toolSpecs); guidance != "" {
		parts = append(parts, guidance)
	}
	if instruction := strings.TrimSpace(contract.Instruction); instruction != "" {
		parts = append(parts, instruction)
	}
	if catalog := skillCatalogBlock(skills); catalog != "" {
		parts = append(parts, catalog)
	}
	if len(contract.IntegrationTools) > 0 {
		parts = append(
			parts,
			"Use the integration tools to communicate with external participants. Ordinary assistant text stays in Omnara.",
		)
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
	projectID uuid.UUID,
	contract agentconfig.RuntimeContract,
) ([]skillstore.SkillRecord, error) {
	if len(contract.Skills) == 0 {
		return nil, nil
	}
	records := make([]skillstore.SkillRecord, 0, len(contract.Skills))
	for _, skill := range contract.Skills {
		record, err := store.GetSkillForDispatch(ctx, projectID, skill.ID)
		if storeerr.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("resolve skill %s for catalog: %w", skill.ID, err)
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
