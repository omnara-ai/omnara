package modelcontext

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
)

// MaxResolvedMediaBytes is Omnara's per-model-request cap for decoded media.
const MaxResolvedMediaBytes int64 = 24 * 1024 * 1024

var ErrOpeningMediaBudgetExceeded = errors.New("opening media exceeds the resolved-media byte budget")

type ResolvedMedia struct {
	ArtifactID string `json:"artifact_id"`
	Kind       string `json:"kind"`
	MediaType  string `json:"media_type"`
	Filename   string `json:"filename,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	Data       []byte `json:"-"`
}

// Opening media is mandatory; historical media is selected newest-first.
func (b Builder) resolveMedia(ctx context.Context, bundle *Bundle, projector MediaProjector) error {
	occurrences := collectMediaOccurrences(bundle)
	if len(occurrences) == 0 {
		return nil
	}
	ids := uniqueMediaArtifactIDs(occurrences)
	records, err := b.Store.ListAgentArtifactsByIDs(ctx, bundle.ProjectID, bundle.AgentID, ids)
	if err != nil {
		return err
	}
	byID := make(map[storage.ID]artifactstore.ArtifactRecord, len(records))
	for _, record := range records {
		if !IsAttachmentMedia(record.ContentType) {
			continue
		}
		byID[record.ID] = record
	}
	metadata := make(map[string]ResolvedMedia, len(byID))
	for _, record := range byID {
		kind, _ := AttachmentKindForMediaType(record.ContentType)
		size := int64(0)
		if record.SizeBytes != nil {
			size = *record.SizeBytes
		}
		metadata[record.ID.String()] = ResolvedMedia{
			ArtifactID: record.ID.String(),
			Kind:       kind,
			MediaType:  record.ContentType,
			Filename:   record.Filename,
			SizeBytes:  size,
		}
	}
	bundle.ResolvedMedia = metadata
	projectedByOccurrence := make(map[MediaOccurrenceRef]RenderedMedia, len(occurrences))
	if projector == nil {
		for _, occurrence := range occurrences {
			media, ok := metadata[occurrence.artifactID.String()]
			if !ok {
				continue
			}
			projectedByOccurrence[occurrence.ref()] = RenderedMedia{
				Occurrence:     occurrence.ref(),
				Media:          media,
				Representation: MediaRepresentationInline,
			}
		}
	} else {
		for _, rendered := range projector.ProjectRenderedMedia(*bundle) {
			if !rendered.Occurrence.valid(*bundle) {
				continue
			}
			projectedByOccurrence[rendered.Occurrence] = rendered
		}
	}

	resolved := make(map[string]ResolvedMedia, len(byID))
	selectedRecords := make(map[storage.ID]artifactstore.ArtifactRecord, len(byID))
	selectedIDs := make([]storage.ID, 0, len(byID))
	loadData := make(map[storage.ID]bool, len(byID))
	var totalBytes int64
	var omittedCount int
	var omittedBytes int64
	selectOccurrence := func(index int) error {
		occurrence := &occurrences[index]
		record, ok := byID[occurrence.artifactID]
		if !ok {
			return nil
		}
		projection, rendered := projectedByOccurrence[occurrence.ref()]
		if !rendered || projection.Media.ArtifactID != record.ID.String() {
			return nil
		}
		switch projection.Representation {
		case MediaRepresentationInline, MediaRepresentationInlineText:
		default:
			return nil
		}
		size := int64(0)
		if record.SizeBytes != nil {
			size = *record.SizeBytes
		}
		if totalBytes+size > MaxResolvedMediaBytes {
			if occurrence.opening {
				return fmt.Errorf(
					"%w: current opening media requires more than %d bytes",
					ErrOpeningMediaBudgetExceeded,
					MaxResolvedMediaBytes,
				)
			}
			omittedCount++
			omittedBytes += size
			return nil
		}
		occurrence.selected = true
		totalBytes += size
		if _, selected := selectedRecords[record.ID]; !selected {
			selectedRecords[record.ID] = record
			selectedIDs = append(selectedIDs, record.ID)
		}
		loadData[record.ID] = true
		return nil
	}
	for index := range occurrences {
		if !occurrences[index].opening {
			continue
		}
		if err := selectOccurrence(index); err != nil {
			return err
		}
	}
	for index := len(occurrences) - 1; index >= 0; index-- {
		if occurrences[index].opening {
			continue
		}
		if err := selectOccurrence(index); err != nil {
			return err
		}
	}
	if err := rewriteOmittedMediaOccurrences(bundle, occurrences, byID); err != nil {
		return err
	}
	for _, id := range selectedIDs {
		record := selectedRecords[id]
		var content []byte
		if loadData[id] {
			var err error
			content, _, err = b.Store.GetArtifactBlob(ctx, bundle.ProjectID, record.AgentID, record.ID)
			if err != nil {
				return fmt.Errorf("load media artifact %s content: %w", record.ID, err)
			}
		}
		kind, _ := AttachmentKindForMediaType(record.ContentType)
		size := int64(0)
		if record.SizeBytes != nil {
			size = *record.SizeBytes
		}
		resolved[record.ID.String()] = ResolvedMedia{
			ArtifactID: record.ID.String(),
			Kind:       kind,
			MediaType:  record.ContentType,
			Filename:   record.Filename,
			SizeBytes:  size,
			Data:       content,
		}
	}
	if len(resolved) > 0 {
		bundle.ResolvedMedia = resolved
	} else {
		bundle.ResolvedMedia = nil
	}
	logent.ModelContextMediaOmitted(ctx, omittedCount, omittedBytes, MaxResolvedMediaBytes)
	return nil
}

type mediaContentOwner struct {
	kind     mediaOccurrenceOwner
	index    int
	sequence int64
	opening  bool
}

type mediaOccurrence struct {
	owner      mediaContentOwner
	partIndex  int
	artifactID storage.ID
	opening    bool
	selected   bool
}

func (o mediaOccurrence) ref() MediaOccurrenceRef {
	return MediaOccurrenceRef{
		ownerKind:  o.owner.kind,
		ownerIndex: o.owner.index,
		partIndex:  o.partIndex,
	}
}

func collectMediaOccurrences(bundle *Bundle) []mediaOccurrence {
	owners := orderedMediaContentOwners(bundle)
	var occurrences []mediaOccurrence
	for _, owner := range owners {
		content, err := owner.content(bundle)
		if err != nil {
			continue
		}
		var parts []map[string]json.RawMessage
		if err := json.Unmarshal(content, &parts); err != nil {
			continue
		}
		for partIndex, part := range parts {
			var partType, artifactID string
			if err := json.Unmarshal(part["type"], &partType); err != nil || partType != "media_ref" {
				continue
			}
			if err := json.Unmarshal(part["artifact_id"], &artifactID); err != nil {
				continue
			}
			id, err := storage.ParseID(artifactID)
			if err != nil || id == storage.NilID {
				continue
			}
			occurrences = append(occurrences, mediaOccurrence{
				owner:      owner,
				partIndex:  partIndex,
				artifactID: id,
				opening:    owner.opening,
			})
		}
	}
	return occurrences
}

func orderedMediaContentOwners(bundle *Bundle) []mediaContentOwner {
	openingInputs := make(map[string]bool, len(bundle.OpeningInputIDs))
	for _, id := range bundle.OpeningInputIDs {
		openingInputs[id.String()] = true
	}
	messages := make([]mediaContentOwner, 0, len(bundle.Messages))
	for index, message := range bundle.Messages {
		messages = append(messages, mediaContentOwner{
			kind:     mediaOccurrenceOwnerMessage,
			index:    index,
			sequence: message.Sequence,
			opening:  openingInputs[message.AgentInputID],
		})
	}
	toolResults := make([]mediaContentOwner, 0, len(bundle.ToolResults))
	for index, result := range bundle.ToolResults {
		toolResults = append(toolResults, mediaContentOwner{
			kind:     mediaOccurrenceOwnerToolResult,
			index:    index,
			sequence: result.SourceEventSequence,
		})
	}
	owners := append(messages, toolResults...)
	sort.SliceStable(owners, func(i, j int) bool {
		return owners[i].sequence < owners[j].sequence
	})
	return owners
}

func (o mediaContentOwner) content(bundle *Bundle) (json.RawMessage, error) {
	switch o.kind {
	case mediaOccurrenceOwnerMessage:
		return bundle.Messages[o.index].Content, nil
	case mediaOccurrenceOwnerToolResult:
		return bundle.ToolResults[o.index].ContentParts, nil
	default:
		return nil, fmt.Errorf("invalid media content owner %d", o.kind)
	}
}

func (o mediaContentOwner) setContent(bundle *Bundle, content json.RawMessage) error {
	switch o.kind {
	case mediaOccurrenceOwnerMessage:
		bundle.Messages[o.index].Content = content
	case mediaOccurrenceOwnerToolResult:
		bundle.ToolResults[o.index].ContentParts = content
	default:
		return fmt.Errorf("invalid media content owner %d", o.kind)
	}
	return nil
}

func (r MediaOccurrenceRef) valid(bundle Bundle) bool {
	if r.partIndex < 0 || r.ownerIndex < 0 {
		return false
	}
	switch r.ownerKind {
	case mediaOccurrenceOwnerMessage:
		return r.ownerIndex < len(bundle.Messages)
	case mediaOccurrenceOwnerToolResult:
		return r.ownerIndex < len(bundle.ToolResults)
	default:
		return false
	}
}

func (r MediaOccurrenceRef) owner(bundle Bundle) (mediaContentOwner, bool) {
	if !r.valid(bundle) {
		return mediaContentOwner{}, false
	}
	if r.ownerKind == mediaOccurrenceOwnerMessage {
		return mediaContentOwner{
			kind:     r.ownerKind,
			index:    r.ownerIndex,
			sequence: bundle.Messages[r.ownerIndex].Sequence,
		}, true
	}
	return mediaContentOwner{
		kind:     r.ownerKind,
		index:    r.ownerIndex,
		sequence: bundle.ToolResults[r.ownerIndex].SourceEventSequence,
	}, true
}

func uniqueMediaArtifactIDs(occurrences []mediaOccurrence) []storage.ID {
	seen := make(map[storage.ID]bool, len(occurrences))
	ids := make([]storage.ID, 0, len(occurrences))
	for _, occurrence := range occurrences {
		if seen[occurrence.artifactID] {
			continue
		}
		seen[occurrence.artifactID] = true
		ids = append(ids, occurrence.artifactID)
	}
	return ids
}

func rewriteOmittedMediaOccurrences(
	bundle *Bundle,
	occurrences []mediaOccurrence,
	records map[storage.ID]artifactstore.ArtifactRecord,
) error {
	for _, occurrence := range occurrences {
		if occurrence.selected {
			continue
		}
		if _, resolvable := records[occurrence.artifactID]; !resolvable {
			continue
		}
		if err := ReplaceMediaOccurrenceWithText(bundle, occurrence.ref()); err != nil {
			return fmt.Errorf("replace omitted media occurrence: %w", err)
		}
	}
	return nil
}

func MediaRefText(part map[string]json.RawMessage) string {
	var artifactID string
	if json.Unmarshal(part["artifact_id"], &artifactID) != nil || artifactID == "" {
		return ""
	}
	return "A prior attachment with artifact ID " + ArtifactPublicID(artifactID) +
		" is not included in the current model context."
}

func ArtifactPublicID(artifactID string) string {
	id, err := storage.ParseID(artifactID)
	if err != nil || id == storage.NilID {
		return artifactID
	}
	publicArtifactID, err := publicid.Encode(publicid.KindArtifact, id)
	if err != nil {
		return artifactID
	}
	return publicArtifactID
}

func ResolvedMediaOccurrences(bundle Bundle) []ResolvedMediaOccurrence {
	var out []ResolvedMediaOccurrence
	for _, owner := range orderedMediaContentOwners(&bundle) {
		content, err := owner.content(&bundle)
		if err != nil {
			continue
		}
		for _, part := range mediaRefParts(content) {
			media, ok := bundle.ResolvedMedia[part.ArtifactID]
			if !ok {
				continue
			}
			var role modelprotocol.MessageRole
			if owner.kind == mediaOccurrenceOwnerMessage {
				role = bundle.Messages[owner.index].Role
			}
			out = append(out, ResolvedMediaOccurrence{
				Ref: MediaOccurrenceRef{
					ownerKind:  owner.kind,
					ownerIndex: owner.index,
					partIndex:  part.PartIndex,
				},
				Media:       media,
				MessageRole: role,
				Opening:     owner.opening,
			})
		}
	}
	return out
}

func ReplaceMediaOccurrenceWithText(bundle *Bundle, ref MediaOccurrenceRef) error {
	if bundle == nil {
		return errors.New("media occurrence bundle is required")
	}
	owner, ok := ref.owner(*bundle)
	if !ok {
		return errors.New("media occurrence owner is invalid")
	}
	content, err := owner.content(bundle)
	if err != nil {
		return err
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(content, &parts); err != nil {
		return fmt.Errorf("decode media occurrence content: %w", err)
	}
	if ref.partIndex >= len(parts) {
		return fmt.Errorf("media occurrence part %d is out of range", ref.partIndex)
	}
	var partType string
	if err := json.Unmarshal(parts[ref.partIndex]["type"], &partType); err != nil || partType != "media_ref" {
		return fmt.Errorf("content part %d is not a media_ref", ref.partIndex)
	}
	textJSON, err := json.Marshal(MediaRefText(parts[ref.partIndex]))
	if err != nil {
		return fmt.Errorf("encode textual media reference: %w", err)
	}
	parts[ref.partIndex] = map[string]json.RawMessage{
		"type": json.RawMessage(`"text"`),
		"text": textJSON,
	}
	content, err = json.Marshal(parts)
	if err != nil {
		return fmt.Errorf("encode media occurrence content: %w", err)
	}
	return owner.setContent(bundle, content)
}

type mediaRefPart struct {
	Type       string `json:"type"`
	ArtifactID string `json:"artifact_id"`
	PartIndex  int    `json:"-"`
}

func mediaRefParts(raw json.RawMessage) []mediaRefPart {
	var parts []mediaRefPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	var out []mediaRefPart
	for index, part := range parts {
		if part.Type == "media_ref" {
			part.PartIndex = index
			out = append(out, part)
		}
	}
	return out
}
